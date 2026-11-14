package loi

import (
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
)

const (
	// mainLobbyID is the id of the lobby that always exists. A client that
	// connects without choosing a lobby lands there.
	mainLobbyID = "main"
	// mainLobbyName is the display name of the main lobby.
	mainLobbyName = "Main"

	// maxLobbyNameLength bounds a lobby name, in characters.
	maxLobbyNameLength = 40
	// lobbyIDLength and lobbyIDAlphabet shape the id of a created lobby.
	lobbyIDLength   = 8
	lobbyIDAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	// Close codes sent with lobbyNotFound and lobbyClosed.
	closeLobbyNotFound = 4404
	closeLobbyClosed   = 4410
	// closeGrace is how long a closed connection may take to answer the close
	// frame before the server stops reading from it.
	closeGrace = 2 * time.Second
)

// Errors returned by the LobbyRegistry methods.
var (
	ErrLobbyNotFound   = errors.New("lobby not found")
	ErrLobbyLimit      = errors.New("too many open lobbies")
	ErrLobbyPermanent  = errors.New("the main lobby can't be closed")
	ErrLobbyInProgress = errors.New("a game is in progress in this lobby")
)

// LobbyRegistry manages the open lobbies. It backs /api/lois/lobbies.
type LobbyRegistry interface {
	// ListLobbies returns main first, then the other lobbies by creation.
	ListLobbies() []model.LobbySummary
	// CreateLobby opens a lobby. An empty name gets a default "Lobby N".
	CreateLobby(name string) (model.LobbySummary, error)
	// DeleteLobby closes a lobby and disconnects its clients.
	DeleteLobby(id string) error
}

// lobby is one Lois des Norms table: its seats and game, the websocket
// clients watching it and the Discord message that tracks it. Every field is
// guarded by gameManager.mu.
type lobby struct {
	id        string
	name      string
	createdAt time.Time
	// lastActive is the last time the lobby had a game or a seated player, or
	// changed its seats or game. Viewers do not count: the idle sweep removes
	// a lobby that has had neither a game nor a seated player for a while,
	// even when clients are watching it.
	lastActive time.Time

	// gs holds the seats and the game. Its voice-channel fields
	// (AvailablePlayers and the Discord guild and channel) stay empty: the
	// manager owns the voice pool, and stateOf fills them in for a payload.
	gs *model.GameState

	// messageID is the Discord message posted by /ldn for this lobby, if any.
	messageID *string

	conns []*websocket.Conn
}

func newLobby(id, name string, now time.Time) *lobby {
	gs := model.NewDefaultGameState()
	gs.AvailablePlayers = nil
	return &lobby{id: id, name: name, createdAt: now, lastActive: now, gs: &gs}
}

// seatOf returns the seat index of playerID, or -1.
func (l *lobby) seatOf(playerID string) int {
	if playerID == "" {
		return -1
	}
	for i, p := range l.gs.Players {
		if p.Player.ID == playerID {
			return i
		}
	}
	return -1
}

func (l *lobby) hasSeatedPlayer() bool {
	for _, p := range l.gs.Players {
		if p.Player.ID != "" {
			return true
		}
	}
	return false
}

// unseat empties every seat of playerID and reports whether there was one.
func (l *lobby) unseat(playerID string) bool {
	found := false
	for i, p := range l.gs.Players {
		if playerID != "" && p.Player.ID == playerID {
			l.gs.Players[i] = model.NewEmptyGamePlayer()
			found = true
		}
	}
	return found
}

// active reports whether l has a game in progress or a seated player.
// Watching a lobby does not keep it open, so open websockets alone can't hold
// the lobby limit forever.
func (l *lobby) active() bool {
	return l.gs.GameInProgress || l.hasSeatedPlayer()
}

// voicePool is what the bot knows of the configured Discord voice channel.
// Every lobby seats players from this one pool. It is guarded by
// gameManager.mu.
type voicePool struct {
	players     map[string]model.AvailablePlayer
	guildID     string
	guildName   string
	channelID   string
	channelName string
}

// lobbyByID returns the open lobby with that id, or nil. Call with mu held.
func (g *gameManager) lobbyByID(id string) *lobby {
	for _, l := range g.lobbies {
		if l.id == id {
			return l
		}
	}
	return nil
}

// lobbyFor returns the lobby a websocket connection is subscribed to. A nil
// conn (an action that does not come from a websocket) means main. A conn
// that is not registered, because its lobby was deleted or never existed,
// gets nil and its actions are dropped. Call with mu held.
func (g *gameManager) lobbyFor(conn *websocket.Conn) *lobby {
	if conn == nil {
		return g.main
	}
	return g.connLobby[conn]
}

// lobbyByMessage returns the lobby whose /ldn message has that id, or nil.
// Call with mu held.
func (g *gameManager) lobbyByMessage(messageID string) *lobby {
	for _, l := range g.lobbies {
		if l.messageID != nil && *l.messageID == messageID {
			return l
		}
	}
	return nil
}

// seatIndex maps every seated player to their lobby. A player sits in at most
// one lobby; should one sit in two, the lobby with a game in progress wins.
// Call with mu held.
func (g *gameManager) seatIndex() map[string]*lobby {
	seats := make(map[string]*lobby)
	for _, l := range g.lobbies {
		for _, p := range l.gs.Players {
			if p.Player.ID == "" {
				continue
			}
			if cur, ok := seats[p.Player.ID]; ok && cur.gs.GameInProgress {
				continue
			}
			seats[p.Player.ID] = l
		}
	}
	return seats
}

// stateOf builds the GameState payload of l: its seats and game plus the
// voice pool, where a player who sits in another lobby carries seatedIn.
// With withSeated, players seated in a game in progress are added to
// availablePlayers even when they left the voice channel, so the seat select
// always has a matching option. Call with mu held.
func (g *gameManager) stateOf(l *lobby, withSeated bool, seats map[string]*lobby) model.GameState {
	gs := *l.gs
	gs.Players = append([]model.GamePlayer(nil), l.gs.Players...)
	gs.AvailablePlayers = make(map[string]model.AvailablePlayer, len(g.pool.players))
	for id, ap := range g.pool.players {
		if other := seats[id]; other != nil && other != l {
			ap.SeatedIn = &model.SeatedIn{ID: other.id, Name: other.name, GameInProgress: other.gs.GameInProgress}
		}
		gs.AvailablePlayers[id] = ap
	}
	gs.DiscordGuildID = g.pool.guildID
	gs.DiscordGuildName = g.pool.guildName
	gs.DiscordGuildChannelID = g.pool.channelID
	gs.DiscordGuildChannelName = g.pool.channelName
	gs.LobbyID = l.id
	gs.LobbyName = l.name
	if withSeated && gs.GameInProgress {
		for _, p := range gs.Players {
			if p.Player.ID == "" {
				continue
			}
			if _, ok := gs.AvailablePlayers[p.Player.ID]; !ok {
				id := p.Player.ID
				gs.AvailablePlayers[id] = model.AvailablePlayer{ID: &id, Name: p.Player.Name}
			}
		}
	}
	return gs
}

// summaryOf builds the REST summary of l. Call with mu held.
func summaryOf(l *lobby) model.LobbySummary {
	players := make([]model.LobbyPlayer, 0, len(l.gs.Players))
	for _, p := range l.gs.Players {
		if p.Player.ID != "" {
			players = append(players, model.LobbyPlayer{ID: p.Player.ID, Name: p.Player.Name})
		}
	}
	return model.LobbySummary{
		ID:             l.id,
		Name:           l.name,
		IsDefault:      l.id == mainLobbyID,
		GameInProgress: l.gs.GameInProgress,
		RollCount:      l.gs.RollCount,
		GameID:         l.gs.GameID,
		Players:        players,
		Viewers:        len(l.conns),
		CreatedAt:      l.createdAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
}

// ListLobbies implements LobbyRegistry.
func (g *gameManager) ListLobbies() []model.LobbySummary {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]model.LobbySummary, 0, len(g.lobbies))
	for _, l := range g.lobbies {
		out = append(out, summaryOf(l))
	}
	return out
}

// CreateLobby implements LobbyRegistry.
func (g *gameManager) CreateLobby(name string) (model.LobbySummary, error) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if len(g.lobbies) >= g.cfg.maxLobbies {
		return model.LobbySummary{}, ErrLobbyLimit
	}
	name = cleanLobbyName(name)
	if name == "" {
		name = g.defaultLobbyName()
	}
	l := g.addLobby(name)
	logger.ComponentLogger("CreateLobby").Info("lobby created", "lobby_id", l.id, "lobby_name", l.name)
	return summaryOf(l), nil
}

// DeleteLobby implements LobbyRegistry.
func (g *gameManager) DeleteLobby(id string) error {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	l := g.lobbyByID(id)
	switch {
	case l == nil:
		return ErrLobbyNotFound
	case l == g.main:
		return ErrLobbyPermanent
	case l.gs.GameInProgress:
		return ErrLobbyInProgress
	}
	logger.ComponentLogger("DeleteLobby").Info("lobby deleted", "lobby_id", l.id, "viewers", len(l.conns))
	g.removeLobby(l)
	return nil
}

// touch restarts the idle clock of lobbies whose seats or game changed.
// Call with mu held.
func (g *gameManager) touch(lobbies ...*lobby) {
	now := g.cfg.now()
	for _, l := range lobbies {
		l.lastActive = now
	}
}

// addLobby registers a new lobby with a fresh id. Call with mu held.
func (g *gameManager) addLobby(name string) *lobby {
	l := newLobby(g.newLobbyID(), name, g.cfg.now())
	g.lobbies = append(g.lobbies, l)
	return l
}

// removeLobby unregisters l, tells its clients and closes their connections,
// retires its Discord message and refreshes the other lobbies when l had
// seated players. Call with mu held.
func (g *gameManager) removeLobby(l *lobby) {
	log := logger.ComponentLogger("removeLobby")
	g.lobbies = slices.DeleteFunc(g.lobbies, func(x *lobby) bool { return x == l })
	for _, c := range l.conns {
		delete(g.connLobby, c)
		if err := closeConn(c, modelwebsocket.LobbyClosed, l.id, closeLobbyClosed, "lobby closed"); err != nil {
			log.Warn("failed to notify a client of the closed lobby", "lobby_id", l.id, "error", err)
		}
	}
	l.conns = nil
	if l.messageID != nil {
		content := lobbyHeader(l) + "\nThis lobby was closed."
		if _, err := g.dm.Session().ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:         config.Config().Discord.ChannelID,
			ID:              *l.messageID,
			Content:         &content,
			Components:      &[]discordgo.MessageComponent{},
			AllowedMentions: noMentions(),
		}); err != nil {
			log.Error("failed to update the discord message of a closed lobby", "lobby_id", l.id, "error", err)
		}
		l.messageID = nil
	}
	if l.hasSeatedPlayer() {
		g.broadcastAll()
	}
}

// sweepIdleLobbies removes every lobby other than main that has had no game
// and no seated player for the configured idle timeout. Its viewers get
// lobbyClosed, as for a DELETE.
func (g *gameManager) sweepIdleLobbies() {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	now := g.cfg.now()
	for _, l := range slices.Clone(g.lobbies) {
		if l == g.main {
			continue
		}
		if l.active() {
			l.lastActive = now
			continue
		}
		if now.Sub(l.lastActive) >= g.cfg.idleTimeout {
			logger.ComponentLogger("sweepIdleLobbies").Info("removing idle lobby", "lobby_id", l.id, "idle_since", l.lastActive)
			g.removeLobby(l)
		}
	}
}

// newLobbyID returns a random id that no open lobby uses. Call with mu held.
func (g *gameManager) newLobbyID() string {
	for {
		b := make([]byte, lobbyIDLength)
		if _, err := rand.Read(b); err != nil {
			panic(fmt.Errorf("crypto/rand failed: %w", err))
		}
		for i := range b {
			b[i] = lobbyIDAlphabet[int(b[i])%len(lobbyIDAlphabet)]
		}
		if id := string(b); g.lobbyByID(id) == nil {
			return id
		}
	}
}

// defaultLobbyName returns "Lobby N" for the smallest N from 2 (main is the
// first lobby) that no open lobby is named. Call with mu held.
func (g *gameManager) defaultLobbyName() string {
	for n := 2; ; n++ {
		name := fmt.Sprintf("Lobby %d", n)
		if !slices.ContainsFunc(g.lobbies, func(l *lobby) bool { return l.name == name }) {
			return name
		}
	}
}

// lobbyNamePunctuation is the punctuation a lobby name may keep. Everything
// outside letters, digits, spaces and this set is dropped, so a name typed
// on the website can't carry Discord markdown, masked links ([x](url)) or
// mentions (@everyone, <@&role>, <#channel>) into the bot's message.
const lobbyNamePunctuation = "-'.!?#&"

// cleanLobbyName keeps the letters, digits, spaces and lobbyNamePunctuation
// of a requested lobby name, collapses whitespace, trims it and keeps at
// most maxLobbyNameLength characters. An empty result means "use the
// default name".
func cleanLobbyName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), strings.ContainsRune(lobbyNamePunctuation, r):
			return r
		case unicode.IsSpace(r):
			return ' '
		default:
			return -1
		}
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > maxLobbyNameLength {
		name = strings.TrimSpace(string(r[:maxLobbyNameLength]))
	}
	return name
}

// closeConn sends a last server action, then a close frame, and makes the
// server's read loop give up if the client never answers the close. Call
// with mu held: it writes to the connection.
func closeConn(conn *websocket.Conn, action modelwebsocket.Action, content string, code int, reason string) error {
	errs := []error{
		conn.WriteJSON(modelwebsocket.Message{Action: action, Content: content}),
		conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second)),
		conn.UnderlyingConn().SetReadDeadline(time.Now().Add(closeGrace)),
	}
	return errors.Join(errs...)
}
