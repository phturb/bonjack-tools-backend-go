package model

import "gorm.io/gorm"

type Game struct {
	gorm.Model
	Players []GamePlayer     `gorm:"foreignKey:GameID"`
	Rolls   []GamePlayerRoll `gorm:"foreignKey:GameID"`
}

type GamePlayer struct {
	GameID   uint   `gorm:"primaryKey"`
	PlayerID string `gorm:"primaryKey"`
	// GameID references Game.ID
	Game *Game `gorm:"foreignKey:GameID"`
	// Player references Player.ID
	Player *Player `gorm:"foreignKey:PlayerID"`
}

type GamePlayerRoll struct {
	GameID     uint   `gorm:"primaryKey"`
	PlayerID   string `gorm:"primaryKey"`
	RollNumber uint   `gorm:"primaryKey"`
	Role       *string
	ChampionID *string
	LaneRole   *LaneRole `gorm:"foreignKey:Role;references:Name"`
	// GameID references Game.ID
	Game *Game `gorm:"foreignKey:GameID"`
	// PlayerID references Player.ID
	Player *Player `gorm:"foreignKey:PlayerID"`
	// ChampionID references Champion.ID
	Champion *Champion `gorm:"foreignKey:ChampionID"`
}

type Player struct {
	ID             string `gorm:"primaryKey"`
	Name           *string
	GamePlayer     []GamePlayer     `gorm:"foreignKey:PlayerID"`
	GamePlayerRoll []GamePlayerRoll `gorm:"foreignKey:PlayerID"`
	PlayerChampion []PlayerChampion `gorm:"foreignKey:PlayerID"`
}

type PlayerChampion struct {
	PlayerID   string    `gorm:"primaryKey"`
	ChampionID string    `gorm:"primaryKey"`
	Champion   *Champion `gorm:"foreignKey:ID;references:ChampionID"`
	Player     *Player   `gorm:"foreignKey:ID;references:PlayerID"`
}

type Champion struct {
	ID             string `gorm:"primaryKey"`
	Name           string
	Img            string
	GamePlayerRoll []GamePlayerRoll `gorm:"foreignKey:ChampionID"`
	PlayerChampion []PlayerChampion `gorm:"foreignKey:ChampionID"`
	WeeklyChampion []WeeklyChampion `gorm:"foreignKey:ID"`
}

type WeeklyChampion struct {
	ID       string    `gorm:"primaryKey"`
	Champion *Champion `gorm:"foreignKey:ID;references:ID"`
}

type LaneRole struct {
	Name           string           `gorm:"primaryKey"`
	GamePlayerRoll []GamePlayerRoll `gorm:"foreignKey:Role"`
}

type LeagueVersion struct {
	Version string `gorm:"primaryKey"`
}
