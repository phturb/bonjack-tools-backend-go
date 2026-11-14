package loi

import (
	"context"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPickChampionAvoidsTakenThenFallsBack(t *testing.T) {
	pool := []modeldb.Champion{{ID: "1"}, {ID: "2"}}
	taken := map[string]struct{}{"1": {}}
	for i := 0; i < 20; i++ {
		assert.Equal(t, "2", pickChampion(pool, taken).ID)
	}
	taken["2"] = struct{}{}
	assert.Contains(t, []string{"1", "2"}, pickChampion(pool, taken).ID)
}

func TestRollLeavesStateUntouchedWhenNoChampions(t *testing.T) {
	gm, _, mockDeps := SetupTestEnvironment(t)
	// Version exists but the champions table is empty: no pool for anyone.
	require.NoError(t, mockDeps.db.Where("1 = 1").Delete(&modeldb.Champion{}).Error)
	require.NoError(t, mockDeps.db.Where("1 = 1").Delete(&modeldb.WeeklyChampion{}).Error)

	name := "p1"
	gm.mu.Lock()
	gm.main.gs.Players[0].Player = model.DiscordPlayer{ID: "p1", Name: &name}
	gm.pool.players["p1"] = model.AvailablePlayer{ID: &gm.main.gs.Players[0].Player.ID, Name: &name}
	gm.mu.Unlock()

	gm.roll(context.Background(), gm.main)

	gm.mu.RLock()
	defer gm.mu.RUnlock()
	assert.False(t, gm.main.gs.GameInProgress)
	assert.True(t, gm.main.gs.CanRoll)
	assert.Equal(t, uint(0), gm.main.gs.RollCount)
	assert.Nil(t, gm.main.gs.Players[0].Role)
	var games []modeldb.Game
	mockDeps.db.Find(&games)
	assert.Empty(t, games, "no game row must be created for a failed roll")
}

func TestVoiceStateMuteToggleDoesNotReAddPlayer(t *testing.T) {
	gm, mockDM, _ := SetupTestEnvironment(t)
	gm.mu.Lock()
	gm.pool.channelID = "test-channel"
	gm.mu.Unlock()

	member := &discordgo.Member{User: &discordgo.User{ID: "p1"}, Nick: "Player 1"}
	join := &discordgo.VoiceStateUpdate{
		VoiceState: &discordgo.VoiceState{UserID: "p1", ChannelID: "test-channel", Member: member},
	}
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), join)
	gm.mu.RLock()
	assert.Equal(t, "p1", gm.main.gs.Players[0].Player.ID)
	gm.mu.RUnlock()

	// The lobby removes the player on purpose.
	gm.mu.Lock()
	gm.main.gs.Players[0] = model.NewEmptyGamePlayer()
	gm.mu.Unlock()

	mute := &discordgo.VoiceStateUpdate{
		VoiceState:   &discordgo.VoiceState{UserID: "p1", ChannelID: "test-channel", Member: member, SelfMute: true},
		BeforeUpdate: &discordgo.VoiceState{UserID: "p1", ChannelID: "test-channel"},
	}
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), mute)
	gm.mu.RLock()
	assert.Equal(t, "", gm.main.gs.Players[0].Player.ID, "mute toggle must not re-seat the player")
	gm.mu.RUnlock()

	// Leaving with no Member payload must not panic and must drop the player.
	leave := &discordgo.VoiceStateUpdate{
		VoiceState:   &discordgo.VoiceState{UserID: "p1", ChannelID: ""},
		BeforeUpdate: &discordgo.VoiceState{UserID: "p1", ChannelID: "test-channel"},
	}
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), leave)
	gm.mu.RLock()
	_, stillAvailable := gm.pool.players["p1"]
	gm.mu.RUnlock()
	assert.False(t, stillAvailable)
}
