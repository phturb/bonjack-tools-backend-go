package loi

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buttonClick(messageID, customID string, member *discordgo.Member, user *discordgo.User) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:      "interaction-1",
		Type:    discordgo.InteractionMessageComponent,
		GuildID: "test-guild",
		Token:   "token",
		Data:    discordgo.MessageComponentInteractionData{CustomID: customID, ComponentType: discordgo.ButtonComponent},
		Message: &discordgo.Message{ID: messageID},
		Member:  member,
		User:    user,
	}}
}

// In a guild, discordgo fills Interaction.Member and leaves Interaction.User
// nil (User is set only in DMs). A Roll, Finish or Cancel click on the /ldn
// message used to dereference the nil User in the handler goroutine and
// crash the process.
func TestInteractionButtonClickFromGuildMember(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1"))
	gm.mu.Lock()
	gm.main.gs.Players[0].Player = model.DiscordPlayer{ID: "player1", Name: stringPtr("Player 1")}
	msgID := "ldn-message"
	gm.main.messageID = &msgID
	gm.mu.Unlock()

	// Neither Member nor User: dismissed, no panic.
	require.NotPanics(t, func() {
		gm.onInteractionCreate(mockDM.Session(), buttonClick(msgID, "roll_btn", nil, nil))
	})
	// A guild member who is not seated: dismissed.
	require.NotPanics(t, func() {
		gm.onInteractionCreate(mockDM.Session(), buttonClick(msgID, "roll_btn", &discordgo.Member{User: &discordgo.User{ID: "player9"}}, nil))
	})
	gm.mu.RLock()
	assert.False(t, gm.main.gs.GameInProgress)
	gm.mu.RUnlock()

	// The seated player clicks Roll in the guild: the game starts.
	require.NotPanics(t, func() {
		gm.onInteractionCreate(mockDM.Session(), buttonClick(msgID, "roll_btn", &discordgo.Member{User: &discordgo.User{ID: "player1"}}, nil))
	})
	gm.mu.RLock()
	assert.True(t, gm.main.gs.GameInProgress)
	assert.EqualValues(t, 1, gm.main.gs.RollCount)
	gm.mu.RUnlock()

	// A DM click carries User only: still resolved. Finish ends the game.
	require.NotPanics(t, func() {
		gm.onInteractionCreate(mockDM.Session(), buttonClick(msgID, "finish_btn", nil, &discordgo.User{ID: "player1"}))
	})
	gm.mu.RLock()
	assert.False(t, gm.main.gs.GameInProgress)
	gm.mu.RUnlock()
}

func TestInteractionUserID(t *testing.T) {
	member := &discordgo.Member{User: &discordgo.User{ID: "m"}}
	user := &discordgo.User{ID: "u"}
	ic := func(m *discordgo.Member, u *discordgo.User) *discordgo.InteractionCreate {
		return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{Member: m, User: u}}
	}
	assert.Equal(t, "m", interactionUserID(ic(member, nil)))
	assert.Equal(t, "m", interactionUserID(ic(member, user)))
	assert.Equal(t, "u", interactionUserID(ic(nil, user)))
	assert.Equal(t, "u", interactionUserID(ic(&discordgo.Member{}, user)))
	assert.Equal(t, "", interactionUserID(ic(nil, nil)))
	assert.Equal(t, "", interactionUserID(&discordgo.InteractionCreate{}))
	assert.Equal(t, "", interactionUserID(nil))
}
