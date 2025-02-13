package model

import "gorm.io/gorm"

type Game struct {
	gorm.Model
	Players []GamePlayer     `gorm:"foreignKey:GameID"`
	Rolls   []GamePlayerRoll `gorm:"foreignKey:GameID"`
}

type GamePlayer struct {
	GameID   int     `gorm:"primaryKey"`
	PlayerID string  `gorm:"primaryKey"`
	Player   *Player `gorm:"foreignKey:ID;references:PlayerID"`
	Game     *Game   `gorm:"foreignKey:ID;references:GameID"`
}

type GamePlayerRoll struct {
	GameID     int    `gorm:"primaryKey"`
	PlayerID   string `gorm:"primaryKey"`
	RollNumber int    `gorm:"primaryKey"`
	Role       *string
	ChampionID *string
	Champion   *Champion `gorm:"foreignKey:ID;references:ChampionID"`
	LaneRole   *LaneRole `gorm:"foreignKey:Name;references:Role"`
	Player     *Player   `gorm:"foreignKey:ID;references:PlayerID"`
	Game       *Game     `gorm:"foreignKey:ID;references:GameID"`
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
