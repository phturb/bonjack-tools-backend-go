package loi

import (
	"context"
	"testing"
	"time"

	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"github.com/stretchr/testify/require"
)

// seedGame inserts a game with one roll per player whose roll was made at rolledAt.
func seedGame(t *testing.T, gm *gameManager, rolledAt time.Time, finished bool) uint {
	t.Helper()
	db := gm.d.Database(context.Background())
	game := modeldb.Game{CreatedAt: rolledAt, UpdatedAt: rolledAt}
	if finished {
		f := rolledAt.Add(time.Minute)
		game.FinishedAt = &f
	}
	require.NoError(t, db.Create(&game).Error)
	rolls := []modeldb.GamePlayerRoll{
		{GameID: game.ID, PlayerID: "player1", RollNumber: 1, Role: "TOP", ChampionID: "1", CreatedAt: rolledAt, UpdatedAt: rolledAt},
		{GameID: game.ID, PlayerID: "player2", RollNumber: 1, Role: "JUNGLE", ChampionID: "2", CreatedAt: rolledAt, UpdatedAt: rolledAt},
	}
	require.NoError(t, db.Create(&rolls).Error)
	return game.ID
}

func unfinishedGameIDs(t *testing.T, gm *gameManager) []uint {
	t.Helper()
	var ids []uint
	require.NoError(t, gm.d.Database(context.Background()).Model(&modeldb.Game{}).
		Where("finished_at IS NULL").Order("id").Pluck("id", &ids).Error)
	return ids
}

func TestBootstrapRestoresRecentGame(t *testing.T) {
	gm, _, _ := setupTest(t)
	old := seedGame(t, gm, time.Now().Add(-48*time.Hour), false)
	recent := seedGame(t, gm, time.Now().Add(-10*time.Minute), false)

	require.NoError(t, gm.bootstrap(context.Background()))

	gm.gsMu.RLock()
	defer gm.gsMu.RUnlock()
	require.True(t, gm.gs.GameInProgress)
	require.Equal(t, recent, gm.gs.GameID)
	require.EqualValues(t, 1, gm.gs.RollCount)
	require.Equal(t, []uint{recent}, unfinishedGameIDs(t, gm), "older open game %d must be closed", old)
}

func TestBootstrapSkipsStaleGame(t *testing.T) {
	gm, _, _ := setupTest(t)
	seedGame(t, gm, time.Now().Add(-72*time.Hour), true)
	stale := seedGame(t, gm, time.Now().Add(-restoreMaxAge-time.Hour), false)

	require.NoError(t, gm.bootstrap(context.Background()))

	gm.gsMu.RLock()
	defer gm.gsMu.RUnlock()
	require.False(t, gm.gs.GameInProgress, "game %d is older than restoreMaxAge", stale)
	require.EqualValues(t, 0, gm.gs.GameID)
	require.Empty(t, unfinishedGameIDs(t, gm), "stale game must be closed so later restarts ignore it")
}

func TestBootstrapNoGames(t *testing.T) {
	gm, _, _ := setupTest(t)
	require.NoError(t, gm.bootstrap(context.Background()))
	require.False(t, gm.gs.GameInProgress)
}
