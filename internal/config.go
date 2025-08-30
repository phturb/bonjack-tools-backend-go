package internal

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"

	"github.com/joho/godotenv"
)

type apiKeys struct {
	RiotApiKey string
}

type gameManager struct {
	TimerTime uint
}

type discord struct {
	Token     string
	ChannelID string
	GuildID   string
}

type database struct {
	DatabaseName string
	Username     string
	Password     string
	Host         string
	Port         string
	SSL          string
}

type server struct {
	Port string
}

func newGameManager() gameManager {
	timerTime, err := strconv.Atoi(os.Getenv("TIMER_TIME"))
	if err != nil {
		slog.Warn("failed to find value for TIMER_TIME, using fallback value")
		timerTime = 60 * 1000 * 5
	}
	return gameManager{
		TimerTime: uint(timerTime),
	}
}

type config struct {
	ApiKeys     apiKeys
	GameManager gameManager
	Server      server
	Discord     discord
	Database    database
}

var (
	mu sync.RWMutex
	c  *config
)

func init() {
	err := godotenv.Load()
	if err != nil {
		slog.Error("Error loading .env file")
		panic(err)
	}

	mu.Lock()
	defer mu.Unlock()
	c = &config{
		ApiKeys: apiKeys{
			RiotApiKey: os.Getenv("RIOT_API_KEY"),
		},
		GameManager: newGameManager(),
		Server: server{
			Port: os.Getenv("PORT"),
		},
		Discord: discord{
			Token:     os.Getenv("DISCORD_TOKEN"),
			ChannelID: os.Getenv("DISCORD_CHANNEL_ID"),
			GuildID:   os.Getenv("DISCORD_GUILD_ID"),
		},
		Database: database{
			DatabaseName: os.Getenv("DATABASE_NAME"),
			Username:     os.Getenv("DATABASE_USERNAME"),
			Password:     os.Getenv("DATABASE_PASSWORD"),
			Host:         os.Getenv("DATABASE_HOST"),
			Port:         os.Getenv("DATABASE_PORT"),
			SSL:          os.Getenv("DATABASE_SSL"),
		},
	}
	slog.Info(fmt.Sprintf("'Config' initialized %v", c))
}

func Config() *config {
	mu.RLock()
	defer mu.RUnlock()
	return c
}
