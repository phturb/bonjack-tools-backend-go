package model

import (
	"math/rand"

	dbmodel "github.com/phturb/bonjack-tools-backend-go/model"
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
	return Roles{
		Role("ADC"),
		Role("JUNGLE"),
		Role("SUPPORT"),
		Role("TOP"),
		Role("MID"),
	}
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

func ChampionFromDB(dbc *dbmodel.Champion) *Champion {
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
	ID   *string `json:"id"`
	Name *string `json:"name,omitempty"`
}

type GameState struct {
	Players             []GamePlayer               `json:"players"`
	RollCount           int                        `json:"rollCount"`
	GameInProgress      bool                       `json:"gameInProgress"`
	AvailablePlayers    map[string]AvailablePlayer `json:"availablePlayers"`
	GameId              int                        `json:"gameId"`
	NextRollTimer       int                        `json:"nextRollTimer"`
	CanRoll             bool                       `json:"canRoll"`
	DiscordGuild        string                     `json:"discordGuild"`
	DiscordGuildChannel string                     `json:"discordGuildChannel"`
	LeagueVersion       string                     `json:"leagueVersion"`
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
		RollCount:           0,
		GameInProgress:      false,
		AvailablePlayers:    make(map[string]AvailablePlayer),
		GameId:              0,
		NextRollTimer:       0,
		CanRoll:             true,
		DiscordGuild:        "",
		DiscordGuildChannel: "",
		LeagueVersion:       "",
	}
}
