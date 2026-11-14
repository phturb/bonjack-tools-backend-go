package loi

import (
	"context"
	"fmt"
	"testing"
	"time"

	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"github.com/stretchr/testify/assert"
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

	gm.mu.RLock()
	defer gm.mu.RUnlock()
	require.True(t, gm.main.gs.GameInProgress)
	require.Equal(t, recent, gm.main.gs.GameID)
	require.EqualValues(t, 1, gm.main.gs.RollCount)
	require.Equal(t, []uint{recent}, unfinishedGameIDs(t, gm), "older open game %d must be closed", old)
}

func TestBootstrapSkipsStaleGame(t *testing.T) {
	gm, _, _ := setupTest(t)
	seedGame(t, gm, time.Now().Add(-72*time.Hour), true)
	stale := seedGame(t, gm, time.Now().Add(-restoreMaxAge-time.Hour), false)

	require.NoError(t, gm.bootstrap(context.Background()))

	gm.mu.RLock()
	defer gm.mu.RUnlock()
	require.False(t, gm.main.gs.GameInProgress, "game %d is older than restoreMaxAge", stale)
	require.EqualValues(t, 0, gm.main.gs.GameID)
	require.Empty(t, unfinishedGameIDs(t, gm), "stale game must be closed so later restarts ignore it")
}

func TestBootstrapNoGames(t *testing.T) {
	gm, _, _ := setupTest(t)
	require.NoError(t, gm.bootstrap(context.Background()))
	require.False(t, gm.main.gs.GameInProgress)
}

// seedGameWith inserts an unfinished game whose single roll, made at
// rolledAt, seats players.
func seedGameWith(t *testing.T, gm *gameManager, rolledAt time.Time, players ...string) uint {
	t.Helper()
	db := gm.d.Database(context.Background())
	game := modeldb.Game{CreatedAt: rolledAt, UpdatedAt: rolledAt}
	require.NoError(t, db.Create(&game).Error)
	roles := []string{"TOP", "JUNGLE", "MID", "ADC", "SUPPORT"}
	rolls := make([]modeldb.GamePlayerRoll, 0, len(players))
	for i, p := range players {
		rolls = append(rolls, modeldb.GamePlayerRoll{GameID: game.ID, PlayerID: p, RollNumber: 1, Role: roles[i], ChampionID: fmt.Sprint(i + 1), CreatedAt: rolledAt, UpdatedAt: rolledAt})
	}
	require.NoError(t, db.Create(&rolls).Error)
	return game.ID
}

func TestBootstrapRestoresEveryRecentGameIntoItsOwnLobby(t *testing.T) {
	gm, _, _ := setupTest(t)
	older := seedGameWith(t, gm, time.Now().Add(-30*time.Minute), "player1", "player2")
	newer := seedGameWith(t, gm, time.Now().Add(-10*time.Minute), "player3", "player4")
	stale := seedGameWith(t, gm, time.Now().Add(-restoreMaxAge-time.Hour), "player5")

	require.NoError(t, gm.bootstrap(context.Background()))

	list := gm.ListLobbies()
	require.Len(t, list, 2)
	assert.Equal(t, "main", list[0].ID)
	assert.Equal(t, newer, list[0].GameID, "the newest game goes into main")
	assert.True(t, list[0].GameInProgress)
	assert.Equal(t, fmt.Sprintf("Restored game #%d", older), list[1].Name)
	assert.Equal(t, older, list[1].GameID)
	assert.True(t, list[1].GameInProgress)
	assert.EqualValues(t, 1, list[1].RollCount)
	assert.Equal(t, []string{"player1", "player2", "", "", ""}, seatIDs(gm, gm.lobbyByID(list[1].ID)))
	assert.Equal(t, []string{"player3", "player4", "", "", ""}, seatIDs(gm, gm.main))
	require.Equal(t, []uint{older, newer}, unfinishedGameIDs(t, gm), "stale game %d must be closed", stale)
}

func TestBootstrapKeepsTheNewerOfTwoGamesSharingAPlayer(t *testing.T) {
	gm, _, _ := setupTest(t)
	older := seedGameWith(t, gm, time.Now().Add(-20*time.Minute), "player1", "player2")
	newer := seedGameWith(t, gm, time.Now().Add(-30*time.Minute), "player2", "player3")

	require.NoError(t, gm.bootstrap(context.Background()))

	list := gm.ListLobbies()
	require.Len(t, list, 1)
	assert.Equal(t, newer, list[0].GameID)
	require.Equal(t, []uint{newer}, unfinishedGameIDs(t, gm), "game %d shares player2 and must be closed", older)
}
