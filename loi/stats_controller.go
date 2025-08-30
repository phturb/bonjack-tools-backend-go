package loi

import "github.com/phturb/bonjack-tools-backend-go/model"

type StatsController interface {
	GetPlayers() []model.Player
	GetGames() []model.Game
	GetRolls() []model.GamePlayerRoll
	GetPlayerChampion(id string) interface{}
	UpdatePlayerChampion(id string) interface{}
}
