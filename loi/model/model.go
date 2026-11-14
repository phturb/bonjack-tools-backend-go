// Package model provides data structures and domain models for the "Loi des Norms" game logic.
// These models represent in-memory game state and are distinct from database persistence models.
//
// Key components:
//   - GameState: Represents the current state of an active game
//   - GamePlayer: Player information and assigned champions/roles
//   - Champion: Champion data used in game logic
//   - AvailablePlayer: Players available for game assignment
//   - PlayerStats: Player performance statistics and history
//
// The package handles:
//   - Player and champion data transformation
//   - Role assignment logic and shuffling algorithms
//   - Game state initialization and management
//   - Domain-specific business rules validation
//
// These models are used by the loi package for game logic execution and are
// mapped to/from database models for persistence.
package model

import (
	"math/rand"

	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
)

type DiscordPlayer struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

func NewEmptyDiscordPlayer() DiscordPlayer {
	return DiscordPlayer{
		ID:   "",
		Name: nil,
	}
}

type Role string

func (r *Role) StringPtr() *string {
	rs := ""
	if r == nil {
		return &rs
	}
	rs = string(*r)
	return &rs
}

type Roles []Role

func NewRoleSlice() Roles {
	roles := make(Roles, 0, 5)
	for _, r := range modeldb.NewRoleSlice() {
		roles = append(roles, Role(r.Name))
	}
	return roles
}

func (rs Roles) Shuffle() Roles {
	for i := range rs {
		j := rand.Intn(i + 1)
		rs[i], rs[j] = rs[j], rs[i]
	}
	return rs
}

type GamePlayer struct {
	Player   DiscordPlayer `json:"player"`
	Role     *Role         `json:"role"`
	Champion *Champion     `json:"champion"`
}

type Champion struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Img  string `json:"img"`
}

func ChampionFromDB(dbc *modeldb.Champion) *Champion {
	if dbc == nil {
		return nil
	}
	return &Champion{
		ID:   dbc.ID,
		Name: dbc.Name,
		Img:  dbc.Img,
	}
}

func NewEmptyGamePlayer() GamePlayer {
	return GamePlayer{
		Player:   NewEmptyDiscordPlayer(),
		Role:     nil,
		Champion: nil,
	}
}

type AvailablePlayer struct {
	ID    *string      `json:"id"`
	Name  *string      `json:"name,omitempty"`
	Stats *PlayerStats `json:"stats,omitempty"`
	// SeatedIn is set only in a payload, and only when the player sits in
	// another lobby than the one the payload describes.
	SeatedIn *SeatedIn `json:"seatedIn,omitempty"`
}

// SeatedIn names the lobby another lobby's available player sits in.
type SeatedIn struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	GameInProgress bool   `json:"gameInProgress"`
}

type PlayerStats struct {
	MaxNumberOfRoll int `json:"maxNumberOfRoll"`
	NumberOfGame    int `json:"numberOfGame"`
	TotalRoll       int `json:"totalRoll"`
}

type GameState struct {
	Players                 []GamePlayer               `json:"players"`
	RollCount               uint                       `json:"rollCount"`
	GameInProgress          bool                       `json:"gameInProgress"`
	AvailablePlayers        map[string]AvailablePlayer `json:"availablePlayers"`
	GameID                  uint                       `json:"gameId"`
	NextRollTimer           uint                       `json:"nextRollTimer"`
	CanRoll                 bool                       `json:"canRoll"`
	DiscordGuildID          string                     `json:"discordGuild"`
	DiscordGuildName        string                     `json:"discordGuildName"`
	DiscordGuildChannelID   string                     `json:"discordGuildChannelId"`
	DiscordGuildChannelName string                     `json:"discordGuildChannel"`
	LeagueVersion           string                     `json:"leagueVersion"`
	LobbyID                 string                     `json:"lobbyId"`
	LobbyName               string                     `json:"lobbyName"`
}

// LobbyPlayer is an occupied seat in a LobbySummary.
type LobbyPlayer struct {
	ID   string  `json:"id"`
	Name *string `json:"name,omitempty"`
}

// LobbySummary is one row of GET /api/lois/lobbies.
type LobbySummary struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	IsDefault      bool          `json:"isDefault"`
	GameInProgress bool          `json:"gameInProgress"`
	RollCount      uint          `json:"rollCount"`
	GameID         uint          `json:"gameId"`
	Players        []LobbyPlayer `json:"players"`   // occupied seats only, never null
	Viewers        int           `json:"viewers"`   // open websocket connections
	CreatedAt      string        `json:"createdAt"` // RFC 3339, UTC, millisecond precision
}

func NewDefaultGameState() GameState {
	return GameState{
		Players: []GamePlayer{
			NewEmptyGamePlayer(),
			NewEmptyGamePlayer(),
			NewEmptyGamePlayer(),
			NewEmptyGamePlayer(),
			NewEmptyGamePlayer(),
		},
		RollCount:               0,
		GameInProgress:          false,
		AvailablePlayers:        make(map[string]AvailablePlayer),
		GameID:                  0,
		NextRollTimer:           0,
		CanRoll:                 true,
		DiscordGuildID:          "",
		DiscordGuildName:        "",
		DiscordGuildChannelName: "",
		DiscordGuildChannelID:   "",
		LeagueVersion:           "",
	}
}
