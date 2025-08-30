package loi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	sharedmodel "github.com/phturb/bonjack-tools-backend-go/model"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type MockDiscordManager struct {
	mock.Mock
	session *discordgo.Session
}

func (m *MockDiscordManager) Session() *discordgo.Session {
	return m.session
}

func (m *MockDiscordManager) GetConfigChannel() (*discordgo.Channel, error) {
	args := m.Called()
	return args.Get(0).(*discordgo.Channel), args.Error(1)
}

type MockDependencies struct {
	db *gorm.DB
}

func (m *MockDependencies) Database(ctx context.Context) *gorm.DB {
	return m.db
}

func (m *MockDependencies) Cron() *cron.Cron {
	return cron.New()
}

func setupTest(t *testing.T) (*gameManager, *MockDiscordManager, *MockDependencies) {
	internal.LoadConfig("../.env.test")
	// Setup mock discord manager
	mockDM := &MockDiscordManager{
		session: &discordgo.Session{
			State: &discordgo.State{},
		},
	}

	// Setup mock dependencies with an in-memory sqlite database
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	assert.NoError(t, err)

	// Auto-migrate the schema
	err = db.AutoMigrate(
		&sharedmodel.Game{},
		&sharedmodel.Player{},
		&sharedmodel.GamePlayer{},
		&sharedmodel.GamePlayerRoll{},
		&sharedmodel.Champion{},
		&sharedmodel.PlayerChampion{},
		&sharedmodel.WeeklyChampion{},
		&sharedmodel.LaneRole{},
		&sharedmodel.LeagueVersion{},
	)
	assert.NoError(t, err)

	// Seed the database with a league version
	db.Create(&sharedmodel.LeagueVersion{Version: "14.1.1"})

	// Seed the database with some champions
	db.Create(&sharedmodel.Champion{ID: "1", Name: "Ashe", Img: "Ashe.png"})
	db.Create(&sharedmodel.Champion{ID: "2", Name: "Garen", Img: "Garen.png"})
	db.Create(&sharedmodel.Champion{ID: "3", Name: "Ryze", Img: "Ryze.png"})
	db.Create(&sharedmodel.Champion{ID: "4", Name: "Annie", Img: "Annie.png"})
	db.Create(&sharedmodel.Champion{ID: "5", Name: "Warwick", Img: "Warwick.png"})

	mockDeps := &MockDependencies{
		db: db,
	}

	// Create the game manager
	gm := NewGameManager(mockDeps, mockDM).(*gameManager)

	// Set a logger
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	// Simulate the onDiscordReady event to set the guild ID
	ready := &discordgo.Ready{
		Guilds: []*discordgo.Guild{
			{
				ID:   "test-guild",
				Name: "Test Guild",
			},
		},
	}
	gm.onDiscordReady(mockDM.Session(), ready)

	return gm, mockDM, mockDeps
}

func TestE2EGameFlow(t *testing.T) {
	gm, mockDM, mockDeps := setupTest(t)

	// 1. Players joining the discord server
	t.Run("Players join discord", func(t *testing.T) {
		// Simulate a GuildCreate event
		guildCreate := &discordgo.GuildCreate{
			Guild: &discordgo.Guild{
				ID:   "test-guild",
				Name: "Test Guild",
				Channels: []*discordgo.Channel{
					{
						ID:   "test-channel",
						Name: "Test Channel",
					},
				},
				VoiceStates: []*discordgo.VoiceState{
					{UserID: "player1", ChannelID: "test-channel"},
					{UserID: "player2", ChannelID: "test-channel"},
				},
				Members: []*discordgo.Member{
					{User: &discordgo.User{ID: "player1"}, Nick: "Player 1"},
					{User: &discordgo.User{ID: "player2"}, Nick: "Player 2"},
				},
			},
		}

		// Set internal config for discord
		internal.Config().Discord.GuildID = "test-guild"
		internal.Config().Discord.ChannelID = "test-channel"

		gm.onGuildCreate(mockDM.Session(), guildCreate)

		// Assert that available players are updated
		gm.gsMu.RLock()
		assert.Len(t, gm.gs.AvailablePlayers, 2)
		assert.Contains(t, gm.gs.AvailablePlayers, "player1")
		assert.Contains(t, gm.gs.AvailablePlayers, "player2")
		gm.gsMu.RUnlock()

		// Assert that players are in the database
		var players []sharedmodel.Player
		mockDeps.db.Find(&players)
		assert.Len(t, players, 2)
	})

	// 2. Getting the players updated for the current game
	t.Run("Update players for the game", func(t *testing.T) {
		updateContent := []map[string]interface{}{
			{"id": "player1"},
			{"id": "player2"},
			{"id": "player3"}, // This player is not in available players
			{"id": "player4"},
			{"id": "player5"},
		}
		updateContentBytes, _ := json.Marshal(updateContent)
		updateMessage := &modelwebsocket.Message{
			Action:  modelwebsocket.UpdatePlayers,
			Content: string(updateContentBytes),
		}

		gm.HandleWebsocketMessage(updateMessage, nil, &http.Request{})

		time.Sleep(100 * time.Millisecond) // Allow time for the go routine to execute

		gm.gsMu.RLock()
		assert.Len(t, gm.gs.Players, 5)
		assert.Equal(t, "player1", gm.gs.Players[0].Player.ID)
		assert.Equal(t, "player2", gm.gs.Players[1].Player.ID)
		assert.Equal(t, "", gm.gs.Players[2].Player.ID)
		gm.gsMu.RUnlock()
	})

	// 3. Starting a game with a first roll
	t.Run("Start game with first roll", func(t *testing.T) {
		rollMessage := &modelwebsocket.Message{Action: modelwebsocket.Roll}
		gm.HandleWebsocketMessage(rollMessage, nil, &http.Request{})

		time.Sleep(100 * time.Millisecond) // Allow time for the go routine to execute

		gm.gsMu.RLock()
		assert.True(t, gm.gs.GameInProgress)
		assert.Equal(t, uint(1), gm.gs.RollCount)
		assert.NotNil(t, gm.gs.Players[0].Role)
		assert.NotNil(t, gm.gs.Players[1].Role)
		assert.NotNil(t, gm.gs.Players[0].Champion)
		assert.NotNil(t, gm.gs.Players[1].Champion)
		gameID := gm.gs.GameId
		gm.gsMu.RUnlock()

		// Assert that game data is in the database
		var game sharedmodel.Game
		mockDeps.db.First(&game, gameID)
		assert.NotZero(t, game.ID)

		var gamePlayers []sharedmodel.GamePlayer
		mockDeps.db.Find(&gamePlayers, "game_id = ?", gameID)
		assert.Len(t, gamePlayers, 2)

		var gamePlayerRolls []sharedmodel.GamePlayerRoll
		mockDeps.db.Find(&gamePlayerRolls, "game_id = ?", gameID)
		assert.Len(t, gamePlayerRolls, 2)
	})

	// 4. Getting a second roll
	t.Run("Second roll", func(t *testing.T) {
		// Reset CanRoll for testing purposes
		gm.gsMu.Lock()
		gm.gs.CanRoll = true
		gameID := gm.gs.GameId
		gm.gsMu.Unlock()

		rollMessage := &modelwebsocket.Message{Action: modelwebsocket.Roll}
		gm.HandleWebsocketMessage(rollMessage, nil, &http.Request{})

		time.Sleep(100 * time.Millisecond) // Allow time for the go routine to execute

		gm.gsMu.RLock()
		assert.Equal(t, uint(2), gm.gs.RollCount)
		gm.gsMu.RUnlock()

		// Assert that new rolls are in the database
		var gamePlayerRolls []sharedmodel.GamePlayerRoll
		mockDeps.db.Find(&gamePlayerRolls, "game_id = ?", gameID)
		assert.Len(t, gamePlayerRolls, 4) // 2 players * 2 rolls
	})

	// 5. Finishing the game
	t.Run("Finish the game", func(t *testing.T) {
		gm.gsMu.RLock()
		gameID := gm.gs.GameId
		gm.gsMu.RUnlock()

		cancelMessage := &modelwebsocket.Message{Action: modelwebsocket.Cancel}
		gm.HandleWebsocketMessage(cancelMessage, nil, &http.Request{})

		time.Sleep(100 * time.Millisecond) // Allow time for the go routine to execute

		gm.gsMu.RLock()
		assert.False(t, gm.gs.GameInProgress)
		assert.Equal(t, uint(0), gm.gs.RollCount)
		assert.True(t, gm.gs.CanRoll)
		gm.gsMu.RUnlock()

		// Assert that game data is deleted from the database
		var game sharedmodel.Game
		err := mockDeps.db.First(&game, gameID).Error
		assert.Error(t, err)

		var gamePlayers []sharedmodel.GamePlayer
		mockDeps.db.Find(&gamePlayers, "game_id = ?", gameID)
		assert.Len(t, gamePlayers, 0)

		var gamePlayerRolls []sharedmodel.GamePlayerRoll
		mockDeps.db.Find(&gamePlayerRolls, "game_id = ?", gameID)
		assert.Len(t, gamePlayerRolls, 0)
	})
}
