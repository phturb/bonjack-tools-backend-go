package loi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"github.com/stretchr/testify/assert"
)

func setupTest(t *testing.T) (*gameManager, *MockDiscordManager, *MockDependenciesWithDB) {
	return SetupTestEnvironment(t)
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

		// Config is already set in SetupTestEnvironment

		gm.onGuildCreate(mockDM.Session(), guildCreate)

		// Assert that available players are updated
		gm.gsMu.RLock()
		assert.Len(t, gm.gs.AvailablePlayers, 2)
		assert.Contains(t, gm.gs.AvailablePlayers, "player1")
		assert.Contains(t, gm.gs.AvailablePlayers, "player2")
		gm.gsMu.RUnlock()

		// Assert that players are in the database
		var players []modeldb.Player
		mockDeps.Database(context.Background()).Find(&players)
		assert.Len(t, players, 5) // 5 seeded players from test setup
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
		gameID := gm.gs.GameID
		gm.gsMu.RUnlock()

		// Assert that game data is in the database
		var game modeldb.Game
		mockDeps.db.First(&game, gameID)
		assert.NotZero(t, game.ID)

		var gamePlayers []modeldb.GamePlayer
		mockDeps.db.Find(&gamePlayers, "game_id = ?", gameID)
		assert.Len(t, gamePlayers, 2)

		var gamePlayerRolls []modeldb.GamePlayerRoll
		mockDeps.db.Find(&gamePlayerRolls, "game_id = ?", gameID)
		assert.Len(t, gamePlayerRolls, 2)
	})

	// 4. Getting a second roll
	t.Run("Second roll", func(t *testing.T) {
		// Reset CanRoll for testing purposes
		gm.gsMu.Lock()
		gm.gs.CanRoll = true
		gameID := gm.gs.GameID
		gm.gsMu.Unlock()

		rollMessage := &modelwebsocket.Message{Action: modelwebsocket.Roll}
		gm.HandleWebsocketMessage(rollMessage, nil, &http.Request{})

		time.Sleep(100 * time.Millisecond) // Allow time for the go routine to execute

		gm.gsMu.RLock()
		assert.Equal(t, uint(2), gm.gs.RollCount)
		gm.gsMu.RUnlock()

		// Assert that new rolls are in the database
		var gamePlayerRolls []modeldb.GamePlayerRoll
		mockDeps.db.Find(&gamePlayerRolls, "game_id = ?", gameID)
		assert.Len(t, gamePlayerRolls, 4) // 2 players * 2 rolls
	})

	// 5. Finishing the game
	t.Run("Finish the game", func(t *testing.T) {
		gm.gsMu.RLock()
		gameID := gm.gs.GameID
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
		var game modeldb.Game
		err := mockDeps.db.First(&game, gameID).Error
		assert.Error(t, err)

		var gamePlayers []modeldb.GamePlayer
		mockDeps.db.Find(&gamePlayers, "game_id = ?", gameID)
		assert.Len(t, gamePlayers, 0)

		var gamePlayerRolls []modeldb.GamePlayerRoll
		mockDeps.db.Find(&gamePlayerRolls, "game_id = ?", gameID)
		assert.Len(t, gamePlayerRolls, 0)
	})
}

func TestNewGameManager(t *testing.T) {
	gm, mockDM, mockDeps := setupTest(t)

	// Assert that the game manager is properly initialized
	assert.NotNil(t, gm)
	assert.NotNil(t, mockDM)
	assert.NotNil(t, mockDeps)
	assert.NotNil(t, gm.d)
	assert.NotNil(t, gm.dm)
	assert.NotNil(t, gm.gs)
	assert.Equal(t, uint(0), gm.gs.RollCount)
	assert.False(t, gm.gs.GameInProgress)
	assert.True(t, gm.gs.CanRoll)
}

func TestNewGameManagerWithMinimalSetup(t *testing.T) {
	// Test with full test environment setup
	gm, _, _ := SetupTestEnvironment(t)

	// Assert basic initialization
	assert.NotNil(t, gm)
	assert.NotNil(t, gm.gs)
	assert.Equal(t, 5, len(gm.gs.Players)) // Should have 5 empty players
	assert.True(t, gm.gs.CanRoll)
	assert.False(t, gm.gs.GameInProgress)
}

func TestLockAndUpdateGameState(t *testing.T) {
	// Test with full test environment setup
	gm, _, _ := SetupTestEnvironment(t)

	// Test locking and unlocking behavior
	unlock := gm.lockAndUpdateGameState()

	// The mutex should be locked, so trying to lock again should deadlock
	// We'll test by checking that unlock function works
	assert.NotNil(t, unlock)

	// Unlock the mutex
	unlock()

	// If we can lock again without deadlock, unlock worked correctly
	unlock2 := gm.lockAndUpdateGameState()
	assert.NotNil(t, unlock2)
	unlock2()
}

func TestBroadcast(t *testing.T) {
	// Test with full test environment setup
	gm, _, _ := SetupTestEnvironment(t)

	// Test broadcasting with no connections (should not panic)
	testMessage := map[string]interface{}{"test": "message"}
	err := gm.broadcast(testMessage, nil)

	// With no connections, should not error
	assert.NoError(t, err)

	// Test that connections slice can be manipulated safely
	gm.connsMu.Lock()
	assert.Empty(t, gm.conns)
	gm.connsMu.Unlock()
}

func TestBroadcastState(t *testing.T) {
	// Test with full test environment setup
	gm, _, _ := SetupTestEnvironment(t)

	// Test broadcasting state without messageID (no Discord calls)
	unlock := gm.lockAndUpdateGameState()
	defer unlock()
	gm.gs.GameInProgress = true
	gm.gs.RollCount = 5

	// Test broadcasting state (no message ID = no Discord API calls)
	gm.broadcastState(nil)

	// Verify game state remains consistent
	assert.True(t, gm.gs.GameInProgress)
	assert.Equal(t, uint(5), gm.gs.RollCount)
}

func TestReset(t *testing.T) {
	// Test reset with proper database setup like other tests
	gm, _, _ := setupTest(t)

	ctx := context.Background()

	func() {
		// Set up a game in progress
		unlock := gm.lockAndUpdateGameState()
		defer unlock()
		gm.gs.GameInProgress = true
		gm.gs.RollCount = 5
		gm.gs.CanRoll = false

		// Set a message ID
		messageID := "test-message"
		gm.messageID = &messageID
	}()

	// Call reset
	gm.reset(ctx)

	// Verify game state is reset
	assert.False(t, gm.gs.GameInProgress)
	assert.Equal(t, uint(0), gm.gs.RollCount)
	assert.True(t, gm.gs.CanRoll)
	assert.Nil(t, gm.messageID)
}

func TestRetrieveChampionsForPlayer(t *testing.T) {
	gm, _, mockDeps := SetupTestEnvironment(t)
	ctx := context.Background()

	// Use one of the seeded players instead of creating new one to avoid conflicts
	playerName := "Player 1"
	gamePlayer := model.GamePlayer{
		Player: model.DiscordPlayer{
			ID:   "player1", // Use seeded player
			Name: &playerName,
		},
	}

	// Associate player with one of the seeded champions
	playerChamp := modeldb.PlayerChampion{
		PlayerID:   "player1",
		ChampionID: "1", // Use seeded champion
	}
	err := mockDeps.Database(ctx).Create(&playerChamp).Error
	assert.NoError(t, err)

	// Test champion retrieval
	champions, err := gm.retrieveChampionsForPlayer(ctx, gamePlayer)

	// Verify results
	assert.NoError(t, err)
	assert.NotEmpty(t, champions)

	// Should include player's champion plus weekly champions
	var playerChampions []modeldb.PlayerChampion
	mockDeps.Database(ctx).Find(&playerChampions, "player_id = ?", "player1")
	assert.Len(t, playerChampions, 1)
}

func TestSetPlayersFromMembers(t *testing.T) {
	gm, _, mockDeps := SetupTestEnvironment(t)

	// Create test members using discordgo from the current context
	members := []*discordgo.Member{
		{
			User: &discordgo.User{ID: "player3", GlobalName: "Player 3"},
		},
		{
			User: &discordgo.User{ID: "player4", GlobalName: "Player 4"},
		},
	}

	// Call setPlayersFromMembers
	gm.setPlayersFromMembers(members)

	// Verify available players are updated
	gm.gsMu.RLock()
	assert.Len(t, gm.gs.AvailablePlayers, 2)
	assert.Contains(t, gm.gs.AvailablePlayers, "player3")
	assert.Contains(t, gm.gs.AvailablePlayers, "player4")

	// Verify player data in available players
	player3 := gm.gs.AvailablePlayers["player3"]
	assert.Equal(t, "player3", *player3.ID)
	assert.Equal(t, "Player 3", *player3.Name)
	player4 := gm.gs.AvailablePlayers["player4"]
	assert.Equal(t, "player4", *player4.ID)
	assert.Equal(t, "Player 4", *player4.Name)
	gm.gsMu.RUnlock()

	// Verify players are created in database
	var players []modeldb.Player
	mockDeps.Database(context.Background()).Find(&players)
	assert.Len(t, players, 5) // Only the original 5 seeded players, as setPlayersFromMembers doesn't create new ones
}

func TestHandleUpdatePlayers(t *testing.T) {
	gm, _, _ := SetupTestEnvironment(t)

	// Add some available players (using seeded players)
	player1ID := "player1"
	player2ID := "player2"
	player1Name := "Player 1"
	player2Name := "Player 2"
	availablePlayers := map[string]model.AvailablePlayer{
		"player1": {ID: &player1ID, Name: &player1Name},
		"player2": {ID: &player2ID, Name: &player2Name},
	}

	gm.gsMu.Lock()
	gm.gs.AvailablePlayers = availablePlayers
	gm.gsMu.Unlock()

	// Test update players message
	updateContent := []map[string]interface{}{
		{"id": "player1"},
		{"id": "player2"},
		{"id": ""},        // Empty player
		{"id": "player3"}, // Not in available players
		{"id": ""},
	}
	updateContentBytes, _ := json.Marshal(updateContent)
	updateMessage := &modelwebsocket.Message{
		Action:  modelwebsocket.UpdatePlayers,
		Content: string(updateContentBytes),
	}

	// Test handling the message
	handled := gm.HandleWebsocketMessage(updateMessage, nil, &http.Request{})

	// Verify message was handled
	assert.True(t, handled)

	// Note: Since this runs in a goroutine, we can't easily test the result
	// In a real test, we'd need to mock or wait for the goroutine
}

func TestHandleRoll(t *testing.T) {
	gm, _, _ := SetupTestEnvironment(t)

	// Set up game state to allow rolling
	gm.gsMu.Lock()
	gm.gs.CanRoll = true
	gm.gs.GameID = 1
	gm.gsMu.Unlock()

	// Test roll message
	rollMessage := &modelwebsocket.Message{Action: modelwebsocket.Roll}

	// Test handling the message
	handled := gm.HandleWebsocketMessage(rollMessage, nil, &http.Request{})

	// Verify message was handled
	assert.True(t, handled)

	// Note: The actual rolling happens in a goroutine and has database access
}

func TestHandleReset(t *testing.T) {
	gm, _, _ := SetupTestEnvironment(t)

	// Test reset message
	resetMessage := &modelwebsocket.Message{Action: modelwebsocket.Reset}

	// Test handling the message - should not panic with memory database
	handled := gm.HandleWebsocketMessage(resetMessage, nil, &http.Request{})

	// Verify message was handled
	assert.True(t, handled)
}

func TestHandleRefreshDiscord(t *testing.T) {
	gm, _, _ := SetupTestEnvironment(t)

	// Test refresh discord message
	refreshMessage := &modelwebsocket.Message{Action: modelwebsocket.RefreshDiscord}

	// Test handling the message
	handled := gm.HandleWebsocketMessage(refreshMessage, nil, &http.Request{})

	// Verify message was handled (refresh doesn't access database)
	assert.True(t, handled)
}
