package internal

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

const (
	DevelopmentEnv = "development"
	ProductionEnv  = "production"
)

type apiKeys struct {
	RiotAPIKey string `json:"riotApiKey"`
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
	DatabaseURL  string `json:"databaseUrl"`
	DatabaseName string `json:"databaseName"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Host         string `json:"host"`
	Port         string `json:"port"`
	SSL          string `json:"ssl"`
}

type server struct {
	Port        string   `json:"port"`
	JWTAudience []string `json:"jwtAudience"`
	JWTIssuer   string   `json:"jwtIssuer"`
	JWTSecret   string   `json:"jwtSecret"`
}

type logger struct {
	Format string     `json:"format"`
	Level  slog.Level `json:"level"`
}

const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

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

func newLogger() logger {
	var logLevel slog.Level
	levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
	switch levelStr {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	return logger{
		Format: os.Getenv("LOG_FORMAT"),
		Level:  logLevel,
	}
}

type config struct {
	Environment string `json:"environment"`
	APIKeys     apiKeys
	GameManager gameManager
	Server      server
	Discord     discord
	Database    database
	Logger      logger
}

var (
	mu sync.RWMutex
	c  *config
)

func init() {
	LoadConfig(".env")
}

func LoadConfig(filename string) {
	mu.Lock()
	defer mu.Unlock()
	err := godotenv.Load(filename)
	if err != nil {
		slog.Error("error loading environment file", "filename", filename, "error", err)
	}

	c = &config{
		Environment: os.Getenv("GO_ENV"),
		APIKeys: apiKeys{
			RiotAPIKey: os.Getenv("RIOT_API_KEY"),
		},
		GameManager: newGameManager(),
		Server: server{
			Port:        os.Getenv("PORT"),
			JWTAudience: strings.Split(os.Getenv("JWT_AUDIENCE"), ","),
			JWTIssuer:   os.Getenv("JWT_ISSUER"),
			JWTSecret:   os.Getenv("JWT_SECRET"),
		},
		Discord: discord{
			Token:     os.Getenv("DISCORD_TOKEN"),
			ChannelID: os.Getenv("DISCORD_CHANNEL_ID"),
			GuildID:   os.Getenv("DISCORD_GUILD_ID"),
		},
		Database: database{
			DatabaseURL:  os.Getenv("DATABASE_URL"),
			DatabaseName: os.Getenv("DATABASE_NAME"),
			Username:     os.Getenv("DATABASE_USERNAME"),
			Password:     os.Getenv("DATABASE_PASSWORD"),
			Host:         os.Getenv("DATABASE_HOST"),
			Port:         os.Getenv("DATABASE_PORT"),
			SSL:          os.Getenv("DATABASE_SSL"),
		},
		Logger: newLogger(),
	}
	raw, err := json.Marshal(c)
	if err != nil {
		slog.Info("config initialized without JSON marshaling", "config_data", fmt.Sprintf("%v", c))
	} else {
		slog.Info("config initialized", "config_json", string(raw))
	}
}

func Config() *config {
	mu.RLock()
	defer mu.RUnlock()
	cc := *c
	return &cc
}
