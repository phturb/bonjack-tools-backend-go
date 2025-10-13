package model

import (
	"time"

	"gorm.io/gorm"
)

type Game struct {
	gorm.Model
	Players []Player         `gorm:"many2many:game_players"`
	Rolls   []GamePlayerRoll `gorm:"foreignKey:GameID"`
}

type GamePlayer struct {
	GameID   uint   `gorm:"primaryKey"`
	PlayerID string `gorm:"primaryKey"`
}

type GamePlayerRoll struct {
	GameID     uint   `gorm:"primaryKey"`
	PlayerID   string `gorm:"primaryKey"`
	RollNumber uint   `gorm:"primaryKey"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Role       string
	ChampionID string
	LaneRole   LaneRole `gorm:"foreignKey:Role;references:Name"`
	// PlayerID references Player.ID
	Player Player
	// ChampionID references Champion.ID
	Champion Champion
}

type Player struct {
	ID        string `gorm:"primaryKey"`
	Name      *string
	Champions []Champion `gorm:"many2many:player_champions"`
	Game      []Game     `gorm:"many2many:game_players"`
}

type PlayerChampion struct {
	PlayerID   string `gorm:"primaryKey"`
	ChampionID string `gorm:"primaryKey"`
}

type Champion struct {
	ID      string `gorm:"primaryKey"`
	Name    string
	Img     string
	Players []Player `gorm:"many2many:player_champions"`
}

type WeeklyChampion struct {
	ChampionID string   `gorm:"primaryKey"`
	Champion   Champion `gorm:"foreignKey:ChampionID;references:ID"`
}

type LaneRole struct {
	Name string `gorm:"primaryKey"`
}

type LeagueVersion struct {
	Version string `gorm:"primaryKey"`
}
