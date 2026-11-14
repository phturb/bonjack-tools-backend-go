package loi

import (
	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
)

const (
	// mainLobbyID is the id of the lobby that always exists.
	mainLobbyID = "main"
	// mainLobbyName is the display name of the main lobby.
	mainLobbyName = "Main"
)

// lobby is one Lois des Norms table: its seats and game, the websocket
// clients watching it and the Discord message that tracks it. Its state is
// guarded by gameManager.mu and its conns by gameManager.connsMu.
type lobby struct {
	id   string
	name string

	// gs holds the seats and the game. Its voice-channel fields
	// (AvailablePlayers and the Discord guild and channel) stay empty: the
	// manager owns the voice pool, and stateOf fills them in for a payload.
	gs *model.GameState

	// messageID is the Discord message posted by /ldn for this lobby, if any.
	messageID *string

	conns []*websocket.Conn
}

func newLobby(id, name string) *lobby {
	gs := model.NewDefaultGameState()
	gs.AvailablePlayers = nil
	return &lobby{id: id, name: name, gs: &gs}
}

// voicePool is what the bot knows of the configured Discord voice channel.
// Players are seated from this pool. It is guarded by gameManager.mu.
type voicePool struct {
	players     map[string]model.AvailablePlayer
	guildID     string
	guildName   string
	channelID   string
	channelName string
}

// stateOf builds the GameState payload of l: its seats and game plus the
// voice pool. With withSeated, players seated in a game in progress are
// added to availablePlayers even when they left the voice channel, so the
// seat select always has a matching option.
func (g *gameManager) stateOf(l *lobby, withSeated bool) model.GameState {
	gs := *l.gs
	gs.Players = append([]model.GamePlayer(nil), l.gs.Players...)
	gs.AvailablePlayers = make(map[string]model.AvailablePlayer, len(g.pool.players))
	for id, ap := range g.pool.players {
		gs.AvailablePlayers[id] = ap
	}
	gs.DiscordGuildID = g.pool.guildID
	gs.DiscordGuildName = g.pool.guildName
	gs.DiscordGuildChannelID = g.pool.channelID
	gs.DiscordGuildChannelName = g.pool.channelName
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
