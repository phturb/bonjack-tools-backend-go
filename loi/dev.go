package loi

import (
	"sort"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
)

// DevVoice simulates the Discord voice channel for cmd/loidev, which runs the
// game manager with no Discord connection. It feeds synthetic Discord events
// into the same handlers the bot uses. Development only: the service
// (cmd/main.go) never uses it.
type DevVoice interface {
	// DevVoiceSync makes members (id -> display name) the whole voice
	// channel, like the guild snapshot the bot receives when it connects.
	DevVoiceSync(members map[string]string)
	// DevVoiceJoin simulates a member joining the voice channel.
	DevVoiceJoin(userID, name string)
	// DevVoiceLeave simulates a member leaving the voice channel.
	DevVoiceLeave(userID string)
}

var _ DevVoice = (*gameManager)(nil)

// DevVoiceSync implements DevVoice.
func (g *gameManager) DevVoiceSync(members map[string]string) {
	cfg := config.Config().Discord
	gc := &discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID:       cfg.GuildID,
		Name:     "loidev",
		Channels: []*discordgo.Channel{{ID: cfg.ChannelID, Name: "loidev voice"}},
	}}
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		gc.VoiceStates = append(gc.VoiceStates, &discordgo.VoiceState{UserID: id, ChannelID: cfg.ChannelID})
		gc.Members = append(gc.Members, &discordgo.Member{User: &discordgo.User{ID: id}, Nick: members[id]})
	}
	g.onGuildCreate(g.dm.Session(), gc)
}

// DevVoiceJoin implements DevVoice.
func (g *gameManager) DevVoiceJoin(userID, name string) {
	channelID := config.Config().Discord.ChannelID
	g.onDiscordVoiceStateUpdate(g.dm.Session(), &discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		UserID:    userID,
		ChannelID: channelID,
		Member:    &discordgo.Member{User: &discordgo.User{ID: userID}, Nick: name},
	}})
}

// DevVoiceLeave implements DevVoice.
func (g *gameManager) DevVoiceLeave(userID string) {
	channelID := config.Config().Discord.ChannelID
	g.onDiscordVoiceStateUpdate(g.dm.Session(), &discordgo.VoiceStateUpdate{
		VoiceState:   &discordgo.VoiceState{UserID: userID},
		BeforeUpdate: &discordgo.VoiceState{UserID: userID, ChannelID: channelID},
	})
}
