// Package loi provides the core game management engine for "Loi des Norms" games.
// It handles game state management, player assignments, champion rolling, and real-time
// WebSocket communication with web clients.
//
// Key components:
//   - GameManager: Central coordinator for the lobbies, game state and Discord integration
//   - Lobbies: several games can run at once; each lobby has its own seats, game,
//     websocket clients and Discord message, and every lobby seats players from the
//     one configured voice channel. A player sits in at most one lobby.
//   - GameController: Implements game flow logic and champion assignment
//   - WebSocket handlers for real-time updates and client communication
//   - Statistics tracking and player performance analytics
//
// The package integrates with Discord for voice channel monitoring and player detection,
// manages in-memory game state, and persists game data through the database models.
// It serves as the business logic hub connecting Discord events, player actions,
// and game statistics.
package loi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/phturb/bonjack-tools-backend-go/internal/logger"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/discord"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GameManager interface {
	// HandleWebsocketMessage routes a client action to the connection's lobby.
	HandleWebsocketMessage(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) bool
	// HandleWebsocketConnection subscribes conn to the lobby named by the
	// `lobby` query parameter of r (main when absent), or unsubscribes it
	// when r is nil.
	HandleWebsocketConnection(conn *websocket.Conn, r *http.Request)
	GameController
	LobbyRegistry
}

// managerConfig holds the timings and limits of a game manager.
type managerConfig struct {
	// tick is the period of the roll cooldown and of the idle sweep. Zero
	// starts no ticker: tests call tickRollTimer and sweepIdleLobbies.
	tick time.Duration
	// idleTimeout is how long a lobby other than main may stay with no game
	// and no seated player before it is removed, viewers or not.
	idleTimeout time.Duration
	// maxLobbies is the number of open lobbies, main included.
	maxLobbies int
	// now is the clock of the idle sweep.
	now func() time.Time
}

func defaultManagerConfig() managerConfig {
	return managerConfig{tick: time.Second, idleTimeout: 30 * time.Minute, maxLobbies: 10, now: time.Now}
}

type gameManager struct {
	d internal.Dependencies

	dm discord.DiscordManager

	cfg managerConfig

	// mu guards the voice pool, the lobby registry, connLobby and every
	// lobby, conns included. One lock for everything keeps the checks that
	// span lobbies atomic and leaves no lock order to get wrong. Websocket
	// writes happen with mu held, which serializes them.
	mu        sync.RWMutex
	pool      voicePool
	main      *lobby
	lobbies   []*lobby // main first, then by creation
	connLobby map[*websocket.Conn]*lobby

	*gameController

	dCleanup func()
	// stop ends the ticker goroutine (tests only; the service never stops it).
	stop chan struct{}
}

var _ GameManager = (*gameManager)(nil)

func NewGameManager(d internal.Dependencies, dm discord.DiscordManager) (GameManager, error) {
	return newGameManager(d, dm, defaultManagerConfig()), nil
}

func newGameManager(d internal.Dependencies, dm discord.DiscordManager, cfg managerConfig) *gameManager {
	main := newLobby(mainLobbyID, mainLobbyName, cfg.now())
	gm := &gameManager{
		d:         d,
		dm:        dm,
		cfg:       cfg,
		stop:      make(chan struct{}),
		pool:      voicePool{players: make(map[string]model.AvailablePlayer)},
		main:      main,
		lobbies:   []*lobby{main},
		connLobby: make(map[*websocket.Conn]*lobby),
		gameController: &gameController{
			d: d,
		},
	}
	gm.dCleanup = gm.registryDiscordEventHandlers(dm.Session())

	if err := gm.bootstrap(context.Background()); err != nil {
		logger.ComponentLogger("NewGameManager").Error("failed to restore in-progress game", "error", err)
	}

	if cfg.tick > 0 {
		go gm.runTicker(cfg.tick)
	}
	return gm
}

// runTicker drives every lobby's roll cooldown and the idle sweep.
func (g *gameManager) runTicker(tick time.Duration) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			g.tickRollTimer(uint(tick / time.Millisecond))
			g.sweepIdleLobbies()
		}
	}
}

// tickRollTimer decrements every lobby's roll cooldown by elapsedMs (clamped
// at zero, NextRollTimer is unsigned) and re-enables rolling in a lobby once
// its cooldown reaches zero.
func (g *gameManager) tickRollTimer(elapsedMs uint) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	for _, l := range g.lobbies {
		if l.gs.NextRollTimer > elapsedMs {
			l.gs.NextRollTimer -= elapsedMs
			continue
		}
		l.gs.NextRollTimer = 0
		if !l.gs.CanRoll {
			l.gs.CanRoll = true
			g.broadcastState(l, nil)
		}
	}
}

// lobbyURL is the web page of a lobby.
func lobbyURL(id string) string {
	return "https://tools.bonjack.club/lois-des-norms/" + url.PathEscape(id)
}

var discordMarkdown = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `_`, `\_`, `~`, `\~`, "`", "\\`", `|`, `\|`, `>`, `\>`)

// lobbyHeader is the first line of a lobby's Discord message: its name.
func lobbyHeader(l *lobby) string {
	return fmt.Sprintf("**%s**", discordMarkdown.Replace(l.name))
}

// noMentions keeps a lobby name typed on the website from pinging anyone.
func noMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}

func updateDiscordMessage(messageID string, s *discordgo.Session, l *lobby, gs *model.GameState) error {
	em, c, err := createDiscordMessage(l, gs)
	if err != nil {
		return err
	}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel: config.Config().Discord.ChannelID,
		ID:      messageID,
		Content: &em,
		Components: &[]discordgo.MessageComponent{
			c,
		},
		AllowedMentions: noMentions(),
	})
	if err != nil {
		return err
	}
	return nil
}

func createDiscordMessage(l *lobby, gs *model.GameState) (string, discordgo.MessageComponent, error) {
	if gs == nil || l == nil {
		return "", nil, errors.New("game state is nil")
	}
	description := lobbyHeader(l) + "\n"
	if gs.GameInProgress {
		description += fmt.Sprintf("Loi in progress, roll number : %d\nWeb dashboard: %s", gs.RollCount, lobbyURL(l.id))
	} else {
		description += "Waiting for players ...\nGo see the web dashboard at " + lobbyURL(l.id)
	}
	description += "\n"
	for i, p := range gs.Players {
		name := ""
		if p.Player.Name == nil || *p.Player.Name == "" {
			name = "Empty Spot"
		} else {
			name = *p.Player.Name
		}
		position := fmt.Sprintf("*Player %d*", i+1)
		if p.Role != nil && p.Champion != nil {
			position = fmt.Sprintf("*%s - %s*", p.Champion.Name, *p.Role)
		}
		description += fmt.Sprintf("> %s\t%s\n", position, name)
	}

	return description, discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.Button{
				CustomID: "roll_btn",
				Label:    "Roll",
				Style:    discordgo.PrimaryButton,
				Disabled: !gs.CanRoll,
			},
			discordgo.Button{
				CustomID: "finish_btn",
				Label:    "Finish",
				Style:    discordgo.SuccessButton,
				Disabled: !gs.GameInProgress,
			},
			discordgo.Button{
				CustomID: "cancel_btn",
				Label:    "Cancel",
				Style:    discordgo.DangerButton,
			},
		},
	}, nil
}

// setPlayersFromMembers replaces the voice pool with members. Every lobby
// without a game in progress drops the seated players who are no longer in
// the channel.
func (g *gameManager) setPlayersFromMembers(members []*discordgo.Member) {
	l := logger.ComponentLogger("setPlayersFromMembers")
	ctx := context.Background()
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	aps := make(map[string]model.AvailablePlayer)
	for _, m := range members {
		if m == nil {
			l.Warn("[playersFromMembers] - discord api returned empty members for discord channel")
			continue
		}
		if m.User == nil || m.User.ID == "" {
			l.Warn("[playersFromMembers] - member is missing user, can't identify user id")
			continue
		}
		name := m.DisplayName()
		stats := g.GetPlayerStats(ctx, m.User.ID)
		ap := model.AvailablePlayer{
			ID:   &m.User.ID,
			Name: &name,
			Stats: &model.PlayerStats{
				MaxNumberOfRoll: stats.Stats.MaxNumberOfRoll,
				NumberOfGame:    stats.Stats.NumberOfGame,
				TotalRoll:       stats.Stats.TotalRoll,
			},
		}

		aps[*ap.ID] = ap
		if err := g.d.Database(ctx).Model(&modeldb.Player{}).Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(&modeldb.Player{
			ID:   *ap.ID,
			Name: ap.Name,
		}).Error; err != nil {
			l.Warn("[playersFromMembers] - failed to update player in database : " + err.Error())
			continue
		}
	}

	g.pool.players = aps
	for _, lb := range g.lobbies {
		gs := lb.gs
		if gs.GameInProgress {
			continue
		}
		for i, p := range gs.Players {
			if p.Player.ID == "" {
				continue
			}
			if ap, ok := aps[p.Player.ID]; !ok {
				l.Warn(fmt.Sprintf("[playersFromMembers] - player not found '%s' in game state", p.Player.ID), "lobby_id", lb.id)
				gs.Players[i] = model.NewEmptyGamePlayer()
			} else {
				gs.Players[i].Player.ID = *ap.ID
				gs.Players[i].Player.Name = ap.Name
			}
		}
		for len(gs.Players) < 5 {
			gs.Players = append(gs.Players, model.NewEmptyGamePlayer())
		}
	}
	g.broadcastAll(g.lobbies...)
}

// HandleWebsocketConnection implements GameManager.
func (g *gameManager) HandleWebsocketConnection(conn *websocket.Conn, r *http.Request) {
	l := logger.ComponentLogger("HandleWebsocketConnection")
	if r == nil {
		l.Info("handling websocket disconnection")
		unlock := g.lockAndUpdateGameState()
		defer unlock()
		lb := g.connLobby[conn]
		if lb == nil {
			return
		}
		delete(g.connLobby, conn)
		lb.conns = slices.DeleteFunc(lb.conns, func(c *websocket.Conn) bool { return c == conn })
		return
	}
	id := mainLobbyID
	if r.URL != nil {
		if q := r.URL.Query().Get("lobby"); q != "" {
			id = q
		}
	}
	l.Info("handling websocket connection", "lobby_id", id)
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	lb := g.lobbyByID(id)
	if lb == nil {
		l.Info("websocket asked for an unknown lobby", "lobby_id", id)
		if err := closeConn(conn, modelwebsocket.LobbyNotFound, id, closeLobbyNotFound, "lobby not found"); err != nil {
			l.Warn("failed to send lobbyNotFound", "lobby_id", id, "error", err)
		}
		return
	}
	sgs, err := json.Marshal(g.stateOf(lb, false, g.seatIndex()))
	if err != nil {
		l.Error(fmt.Sprintf("failed to serialized game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	lb.conns = append(lb.conns, conn)
	g.connLobby[conn] = lb
	l.Info("[HandleWebsocketConnection] - sending game state to the new connection")
	if err := conn.WriteJSON(m); err != nil {
		l.Error(err.Error())
	}
}

// broadcast sends m to every client of lb but sender. Call with mu held.
func (g *gameManager) broadcast(lb *lobby, m interface{}, sender *websocket.Conn) error {
	l := logger.ComponentLogger("broadcast")
	l.Info("broadcasting message", "lobby_id", lb.id)
	var errs []error
	for _, c := range lb.conns {
		if c == sender {
			continue
		}
		if err := c.WriteJSON(m); err != nil {
			errs = append(errs, err)
		}
	}
	err := errors.Join(errs...)
	if err != nil {
		l.WithError(err).Error("[broadcast] - failed to broadcast message")
	} else {
		l.Info("message broadcasted successfully")
	}
	return err
}

// HandleWebsocket implements GameManager.
func (g *gameManager) HandleWebsocketMessage(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) bool {
	l := logger.ComponentLogger("HandleWebsocketMessage")
	l.Info(fmt.Sprintf("%s event received", wm.Action))
	switch wm.Action {
	case modelwebsocket.UpdatePlayers:
		go g.handleUpdatePlayers(wm, conn, r)
		return true
	case modelwebsocket.Roll:
		go g.handleRoll(wm, conn, r)
		return true
	case modelwebsocket.Cancel:
		go g.handleCancel(wm, conn, r)
		return true
	case modelwebsocket.Reset:
		go g.handleReset(wm, conn, r)
		return true
	case modelwebsocket.RefreshDiscord:
		go g.handleRefreshDiscord(wm, conn, r)
		return true
	default:
		l.Debug(fmt.Sprintf("websocket action '%s' is not handled by the game manager", wm.Action))
		return false
	}
}

// broadcastState sends lb's state to sender only or, when sender is nil, to
// every client of lb and to lb's Discord message. Call with mu held.
func (g *gameManager) broadcastState(lb *lobby, sender *websocket.Conn) {
	g.publish(lb, sender, sender == nil, g.seatIndex())
}

// broadcastAll sends every lobby its state, because the seats and the game
// status of one lobby show up in the others as seatedIn markers, and
// refreshes the Discord message of each lobby in changed. Call with mu held.
func (g *gameManager) broadcastAll(changed ...*lobby) {
	seats := g.seatIndex()
	for _, lb := range g.lobbies {
		g.publish(lb, nil, slices.Contains(changed, lb), seats)
	}
}

func (g *gameManager) publish(lb *lobby, sender *websocket.Conn, toDiscord bool, seats map[string]*lobby) {
	l := logger.ComponentLogger("broadcastState")
	gs := g.stateOf(lb, true, seats)
	sgs, err := json.Marshal(gs)
	if err != nil {
		l.Error(fmt.Sprintf("failed to marshal game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	if sender != nil {
		if err := sender.WriteJSON(m); err != nil {
			l.Error(fmt.Sprintf("failed to broadcast game state : %s", err.Error()))
		}
	} else {
		if err := g.broadcast(lb, m, nil); err != nil {
			l.Error(fmt.Sprintf("failed to broadcast game state : %s", err.Error()))
		}
	}
	if toDiscord && lb.messageID != nil {
		if err := updateDiscordMessage(*lb.messageID, g.dm.Session(), lb, &gs); err != nil {
			l.Error(fmt.Sprintf("failed to update discord message on broadcast: %s", err.Error()))
		}
	}
}

// handleUpdatePlayers re-seats the connection's lobby. A player who sits in
// another lobby moves here when that lobby is only waiting; when that lobby
// has a game in progress the whole update is refused. A refused update sends
// the true state back to the sender, since the client applied it
// optimistically.
func (g *gameManager) handleUpdatePlayers(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	l := logger.ComponentLogger("handleUpdatePlayers")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	lb := g.lobbyFor(conn)
	if lb == nil {
		l.Info("connection has no lobby, dropping update")
		return
	}
	if lb.gs.GameInProgress {
		l.Warn("[handleUpdatePlayers] - game is in progress, skipping update", "lobby_id", lb.id)
		g.broadcastState(lb, conn)
		return
	}
	l.Warn("[handleUpdatePlayers] - game is not in progress updating players list", "lobby_id", lb.id)

	type content struct {
		ID   string  `json:"id"`
		Name *string `json:"name,omitempty"`
	}
	var cs []content
	if err := json.Unmarshal([]byte(wm.Content), &cs); err != nil {
		l.Error(fmt.Sprintf("[handleUpdatePlayers] - failed to unmarshal content : %s", err.Error()))
		return
	}

	seats := g.seatIndex()
	var gps []model.GamePlayer
	var movedFrom []*lobby
	naps := map[string]content{}
	for _, c := range cs {
		if len(gps) >= 5 {
			l.Warn("[handleUpdatePlayers] dropping any players going beyond 5")
			break
		}
		if c.ID != "" {
			if _, ok := naps[c.ID]; ok {
				l.Warn(fmt.Sprintf("[handleUpdatePlayers] - double entry for player %s, dropping entry", c.ID))
				continue
			}
		}
		if c.ID == "" {
			gps = append(gps, model.NewEmptyGamePlayer())
			continue
		}
		naps[c.ID] = c
		ap, ok := g.pool.players[c.ID]
		if !ok {
			gps = append(gps, model.NewEmptyGamePlayer())
			continue
		}
		if other := seats[c.ID]; other != nil && other != lb {
			if other.gs.GameInProgress {
				l.Warn("player is in a game in another lobby, refusing the update",
					"lobby_id", lb.id, "player_id", c.ID, "other_lobby_id", other.id)
				g.broadcastState(lb, conn)
				return
			}
			movedFrom = append(movedFrom, other)
		}
		gps = append(gps, model.GamePlayer{
			Player: model.DiscordPlayer{
				ID:   c.ID,
				Name: ap.Name,
			},
		})
	}
	for len(gps) < 5 {
		l.Warn("[handleUpdatePlayers] missing player, adding empty one")
		gps = append(gps, model.NewEmptyGamePlayer())
	}
	for _, p := range gps {
		if other := seats[p.Player.ID]; other != nil && other != lb {
			l.Info("moving player from a waiting lobby", "player_id", p.Player.ID, "from_lobby_id", other.id, "to_lobby_id", lb.id)
			other.unseat(p.Player.ID)
		}
	}
	lb.gs.Players = gps
	g.touch(append(movedFrom, lb)...)
	g.broadcastAll(append(movedFrom, lb)...)
}

func (g *gameManager) handleRoll(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if lb := g.lobbyFor(conn); lb != nil {
		g.rollLocked(r.Context(), lb)
	}
}

// pickChampion returns a random champion from pool that is not already taken.
// When every champion in the pool is taken it falls back to any champion of the
// pool rather than failing the roll.
func pickChampion(pool []modeldb.Champion, taken map[string]struct{}) modeldb.Champion {
	free := make([]modeldb.Champion, 0, len(pool))
	for _, c := range pool {
		if _, ok := taken[c.ID]; !ok {
			free = append(free, c)
		}
	}
	if len(free) == 0 {
		free = pool
	}
	return free[rand.Intn(len(free))]
}

func (g *gameManager) roll(ctx context.Context, lb *lobby) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	g.rollLocked(ctx, lb)
}

// rollLocked rolls roles and champions in lb. The first roll starts the game,
// and is refused when a seated player is already playing in another lobby.
// Call with mu held.
func (g *gameManager) rollLocked(ctx context.Context, lb *lobby) {
	l := logger.ComponentLogger("roll")
	if !lb.gs.CanRoll {
		l.Info("game is not allowed to roll", "lobby_id", lb.id)
		return
	}
	if !lb.gs.GameInProgress {
		for _, p := range lb.gs.Players {
			for _, other := range g.lobbies {
				if other != lb && other.gs.GameInProgress && other.seatOf(p.Player.ID) >= 0 {
					l.Warn("a seated player is in a game in another lobby, refusing to start",
						"lobby_id", lb.id, "player_id", p.Player.ID, "other_lobby_id", other.id)
					return
				}
			}
		}
	}
	db := g.d.Database(ctx)
	var lVer modeldb.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		l.Error("failed to retrieve version", "error", err)
		return
	}

	// 1. Decide roles and champions without touching the game state.
	rollNumber := lb.gs.RollCount + 1
	roles := model.NewRoleSlice().Shuffle()
	type assignment struct {
		role     model.Role
		champion *modeldb.Champion
	}
	assignments := make([]assignment, len(lb.gs.Players))
	taken := map[string]struct{}{}
	for i, p := range lb.gs.Players {
		if i >= len(roles) {
			break
		}
		assignments[i].role = roles[i]
		if p.Player.ID == "" {
			continue
		}
		pool, err := g.retrieveChampionsForPlayer(ctx, p)
		if err != nil {
			l.Error("failed to retrieve player champions, aborting roll", "player_id", p.Player.ID, "error", err)
			return
		}
		if len(pool) == 0 {
			l.Error("no champion to select from, aborting roll", "player_id", p.Player.ID)
			return
		}
		rc := pickChampion(pool, taken)
		taken[rc.ID] = struct{}{}
		assignments[i].champion = &rc
		l.Info("assigned champion", "player_id", p.Player.ID, "role", roles[i], "champion", rc.Name)
	}

	// 2. Persist game, players and rolls in one transaction.
	gameID := lb.gs.GameID
	newGame := !lb.gs.GameInProgress
	err := db.Transaction(func(tx *gorm.DB) error {
		if newGame {
			game := modeldb.Game{}
			if err := tx.Model(&modeldb.Game{}).Create(&game).Error; err != nil {
				return err
			}
			gps := make([]modeldb.GamePlayer, 0, len(lb.gs.Players))
			for _, p := range lb.gs.Players {
				if p.Player.ID == "" {
					continue
				}
				gps = append(gps, modeldb.GamePlayer{GameID: game.ID, PlayerID: p.Player.ID})
			}
			if len(gps) > 0 {
				if err := tx.Model(&modeldb.GamePlayer{}).Create(&gps).Error; err != nil {
					return err
				}
			}
			gameID = game.ID
		}
		gprs := make([]modeldb.GamePlayerRoll, 0, len(lb.gs.Players))
		for i, p := range lb.gs.Players {
			if assignments[i].champion == nil {
				continue
			}
			gprs = append(gprs, modeldb.GamePlayerRoll{
				GameID:     gameID,
				PlayerID:   p.Player.ID,
				RollNumber: rollNumber,
				Role:       string(assignments[i].role),
				ChampionID: assignments[i].champion.ID,
			})
		}
		if len(gprs) > 0 {
			if err := tx.Model(&modeldb.GamePlayerRoll{}).Create(&gprs).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		l.Error("failed to persist roll, game state unchanged", "error", err)
		return
	}

	// 3. Commit to the in-memory state only once the database agreed.
	if newGame {
		l.Info("loi des norms has started", "game_id", gameID, "lobby_id", lb.id)
	}
	lb.gs.GameID = gameID
	lb.gs.GameInProgress = true
	lb.gs.LeagueVersion = lVer.Version
	lb.gs.RollCount = rollNumber
	lb.gs.CanRoll = false
	lb.gs.NextRollTimer = config.Config().GameManager.TimerTime
	for i := range lb.gs.Players {
		if i >= len(assignments) {
			break
		}
		role := assignments[i].role
		lb.gs.Players[i].Role = &role
		lb.gs.Players[i].Champion = model.ChampionFromDB(assignments[i].champion)
	}
	l.Info("roll completed", "game_id", gameID, "roll_number", rollNumber, "lobby_id", lb.id)
	g.touch(lb)
	g.broadcastAll(lb)
}

func (g *gameManager) retrieveChampionsForPlayer(ctx context.Context, gp model.GamePlayer) ([]modeldb.Champion, error) {
	l := logger.ComponentLogger("retrieveChampionsForPlayer")
	cs := make([]modeldb.Champion, 0)
	err := g.d.Database(ctx).Transaction(func(tx *gorm.DB) error {
		var p modeldb.Player
		if err := tx.Model(&modeldb.Player{}).Preload("Champions").Where("id = ?", gp.Player.ID).Find(&p).Error; err != nil {
			l.Error(fmt.Sprintf("failed to retrieve player champions : %s", err.Error()))
			return err
		}
		cs = append(cs, p.Champions...)
		if len(cs) <= 0 {
			if err := tx.Model(&modeldb.Champion{}).Find(&cs).Error; err != nil {
				l.Error(fmt.Sprintf("failed to retrieve all champions : %s", err.Error()))
				return err
			}
		}
		wcs := make([]modeldb.WeeklyChampion, 0)
		if err := tx.Model(&modeldb.WeeklyChampion{}).Preload("Champion").Find(&wcs).Error; err != nil {
			l.Error(fmt.Sprintf("failed to retrieve weekly champions : %s", err.Error()))
			return err
		}
		for _, wc := range wcs {
			include := false
			for _, c := range cs {
				if c.ID == wc.Champion.ID {
					include = true
					break
				}
			}
			if !include {
				cs = append(cs, wc.Champion)
			}
		}
		return nil
	})
	return cs, err
}

func (g *gameManager) handleCancel(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if lb := g.lobbyFor(conn); lb != nil {
		g.cancelLocked(r.Context(), lb)
	}
}

func (g *gameManager) cancel(ctx context.Context, lb *lobby) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	g.cancelLocked(ctx, lb)
}

// cancelLocked deletes lb's game, if one is in progress, then resets lb.
// Call with mu held.
func (g *gameManager) cancelLocked(ctx context.Context, lb *lobby) {
	l := logger.ComponentLogger("cancel")
	db := g.d.Database(ctx)
	if lb.gs.GameInProgress && lb.gs.RollCount > 0 {
		l.Info("cancelling the current loi", "lobby_id", lb.id, "game_id", lb.gs.GameID)
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("game_id = ?", lb.gs.GameID).Delete(&modeldb.GamePlayerRoll{}).Error; err != nil {
				return err
			}
			if err := tx.Where("game_id = ?", lb.gs.GameID).Delete(&modeldb.GamePlayer{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", lb.gs.GameID).Delete(&modeldb.Game{}).Error; err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			l.Error("[handleCancel] - " + err.Error())
			return
		}
		lb.gs.GameID = 0
	}
	g.resetLocked(ctx, lb)
}

func (g *gameManager) handleReset(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if lb := g.lobbyFor(conn); lb != nil {
		g.resetLocked(r.Context(), lb)
	}
}

func (g *gameManager) reset(ctx context.Context, lb *lobby) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	g.resetLocked(ctx, lb)
}

// resetLocked finishes lb's game and clears its roles and champions. Seated
// players who left the voice channel lose their seat. Call with mu held.
func (g *gameManager) resetLocked(ctx context.Context, lb *lobby) {
	l := logger.ComponentLogger("reset")
	db := g.d.Database(ctx)
	var lVer modeldb.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		return
	}
	l.Info("resetting the game state values", "lobby_id", lb.id)
	if lb.gs.RollCount != 0 {
		if err := db.Model(&modeldb.Game{}).Where("id = ?", lb.gs.GameID).Update("finished_at", time.Now()).Error; err != nil {
			l.WithError(err).Error("failed to finishedAt game field")
		}
	}
	content := fmt.Sprintf("%s\nLoi is finished or canceled after %d roll", lobbyHeader(lb), lb.gs.RollCount)
	lb.gs.LeagueVersion = lVer.Version
	lb.gs.GameInProgress = false
	lb.gs.RollCount = 0
	lb.gs.NextRollTimer = 0
	lb.gs.CanRoll = true
	if lb.messageID != nil {
		_, err := g.dm.Session().ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel:         config.Config().Discord.ChannelID,
			ID:              *lb.messageID,
			Content:         &content,
			AllowedMentions: noMentions(),
		})
		if err != nil {
			l.WithError(err).Error("failed to update channel message")
		}
	}
	lb.messageID = nil
	l.Info("removing players that are no longer available")
	for i := range lb.gs.Players {
		if _, ok := g.pool.players[lb.gs.Players[i].Player.ID]; !ok {
			lb.gs.Players[i] = model.NewEmptyGamePlayer()
		} else {
			lb.gs.Players[i].Role = nil
			lb.gs.Players[i].Champion = nil
		}
	}
	g.touch(lb)
	g.broadcastAll(lb)
}

func (g *gameManager) handleRefreshDiscord(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
}

func (g *gameManager) lockAndUpdateGameState() func() {
	g.mu.Lock()
	return g.mu.Unlock
}
