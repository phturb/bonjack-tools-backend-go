package internal

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"

	"github.com/joho/godotenv"
)

type apiKeys struct {
	RiotApiKey string `json:"riotApiKey"`
}

type gameManager struct {
	TimerTime uint `json:"timerTime"`
}

type discord struct {
	Token     string `json:"token"`
	ChannelID string `json:"channelId"`
	GuildID   string `json:"guildId"`
}

type database struct {
	DatabaseName string `json:"databaseName"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Host         string `json:"host"`
	Port         string `json:"port"`
	SSL          string `json:"ssl"`
}

type server struct {
	Port string `json:"port"`
}

func newGameManager() gameManager {
	timerTime, err := strconv.Atoi(os.Getenv("TIMER_TIME"))
	if err != nil {
		slog.Warn("[GameManager] - failed to find value for TIMER_TIME, using fallback value")
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
	raw, err := json.Marshal(c)
	if err != nil {
		slog.Info(fmt.Sprintf("[core] - 'Config' initialized %v", c))
	} else {
		slog.Info(fmt.Sprintf("[core] - 'Config' initialized %s", string(raw)))
	}
}

func Config() *config {
	mu.RLock()
	defer mu.RUnlock()
	return c
}
