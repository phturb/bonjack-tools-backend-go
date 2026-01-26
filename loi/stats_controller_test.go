package loi

import (
	"context"
	"testing"

	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	modelserver "github.com/phturb/bonjack-tools-backend-go/model/server"
	"github.com/stretchr/testify/assert"
)

func TestGetPlayers(t *testing.T) {
	// Test with in-memory database instead of nil database
	gm, _, mockDeps := SetupTestEnvironment(t)
	ctx := context.Background()

	// Test get players - should not panic with memory database
	players := gm.GetPlayers(ctx)

	// Should return players without panicking
	assert.NotNil(t, players)

	// Verify we can access the database through the mock dependencies
	db := mockDeps.Database(ctx)
	var allPlayers []modeldb.Player
	err := db.Find(&allPlayers).Error
	assert.NoError(t, err)
	assert.NotEmpty(t, allPlayers) // We seeded test data
}

func TestGetGames(t *testing.T) {
	// Test with in-memory database instead of nil database
	gm, _, _ := SetupTestEnvironment(t)
	ctx := context.Background()

	// Test get games - should not panic with memory database
	games := gm.GetGames(ctx)

	// Should return empty slice (no games created yet, but no panic)
	assert.NotNil(t, games)
	assert.Empty(t, games)
}

func TestGetRolls(t *testing.T) {
	// Test with in-memory database instead of nil database
	gm, _, _ := SetupTestEnvironment(t)
	ctx := context.Background()

	// Test get rolls - should not panic with memory database
	rolls := gm.GetRolls(ctx)

	// Should return empty slice (no rolls created yet, but no panic)
	assert.NotNil(t, rolls)
	assert.Empty(t, rolls)
}

func TestGetPlayerStats(t *testing.T) {
	// Test with in-memory database instead of nil database
	gm, _, _ := SetupTestEnvironment(t)
	ctx := context.Background()

	// Test get player stats - should not panic with memory database
	playerStats := gm.GetPlayerStats(ctx, "player1") // Use seeded player

	// Should return player stats (may be empty for non-existent games, but no panic)
	assert.NotEqual(t, modelserver.PlayerStats{}, playerStats)
}

func TestGetPlayerChampion(t *testing.T) {
	// Test with in-memory database instead of nil database
	gm, _, _ := SetupTestEnvironment(t)
	ctx := context.Background()

	// Test get player champions - should not panic with memory database
	playerChampions := gm.GetPlayerChampion(ctx, "player1") // Use seeded player

	// Should return player champions (may be empty for non-existent associations, but no panic)
	assert.NotNil(t, playerChampions)
}

func TestUpdatePlayerChampion(t *testing.T) {
	// Test with in-memory database instead of nil database
	gm, _, _ := SetupTestEnvironment(t)
	ctx := context.Background()

	// Test update player champions - should not panic with memory database
	updatePayload := []modelserver.UpdatePlayerChampionPayload{
		{ID: "1", Owned: true}, // Use seeded champion IDs
		{ID: "2", Owned: false},
	}

	result := gm.UpdatePlayerChampion(ctx, "player1", updatePayload)

	// Should return updated player champions (may be empty, but no panic)
	assert.NotNil(t, result)
}
