// Package model provides GORM database entities for the bonjack-tools-backend-go service.
// These models define the database schema and handle data persistence for players, games,
// champions, and related entities.
//
// Key entities:
//   - Game: Represents completed games with metadata and statistics
//   - Player: Player information and Discord user details
//   - Champion: League of Legends champion data
//   - GamePlayerRoll: Many-to-many relationship for player-champion assignments
//   - WeeklyChampion: Weekly champion rotation data
//   - LaneRole: Predefined lane roles for champion assignments
//
// Features:
//   - GORM tags for database schema definition
//   - Many-to-many relationships with proper foreign keys
//   - Timestamps for auditing and data lifecycle
//   - JSON serialization support for API responses
//
// These models are used by the database layer for persistence and are mapped
// to/from domain models in the loi package for business logic operations.
package model

import (
	"time"
)

type Game struct {
	ID         uint `gorm:"primarykey"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
	Players    []Player         `gorm:"many2many:game_players"`
	Rolls      []GamePlayerRoll `gorm:"foreignKey:GameID"`
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

func NewRoleSlice() []LaneRole {
	return []LaneRole{
		{"ADC"},
		{"JUNGLE"},
		{"SUPPORT"},
		{"TOP"},
		{"MID"},
	}
}
