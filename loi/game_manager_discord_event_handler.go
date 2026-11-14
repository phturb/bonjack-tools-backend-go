package loi

import (
	"context"
	"fmt"

	"github.com/phturb/bonjack-tools-backend-go/internal/logger"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"gorm.io/gorm/clause"
)

func (gm *gameManager) registryDiscordEventHandlers(session *discordgo.Session) func() {
	handlesRMs := []func(){
		session.AddHandler(gm.onDiscordReady),
		session.AddHandler(gm.onGuildCreate),
		session.AddHandler(gm.onGuildUpdate),
		session.AddHandler(gm.onDiscordVoiceStateUpdate),
		session.AddHandler(gm.onChannelUpdate),
		session.AddHandler(gm.onInteractionCreate),
	}
	return func() {
		for _, rm := range handlesRMs {
			rm()
		}
	}
}

func (g *gameManager) onDiscordReady(s *discordgo.Session, e *discordgo.Ready) {
	l := logger.ComponentLogger("onDiscordReady")
	l.Info("discord ready event received")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	var guild *discordgo.Guild
	for _, g := range e.Guilds {
		if g == nil {
			continue
		}
		if g.ID == config.Config().Discord.GuildID {
			guild = g
			break
		}
	}
	if guild == nil {
		l.Error("no guild found in discord ready event")
		g.pool.guildID = config.Config().Discord.GuildID
	} else {
		l.Info("configured guild found", "guild_name", guild.Name, "guild_id", guild.ID)
		g.pool.guildID = guild.ID
		g.pool.guildName = guild.Name

	}
	for _, cmd := range []*discordgo.ApplicationCommand{
		{
			Name:        "ldn",
			Description: "Creates the discord lois des norms",
		},
	} {
		_, err := s.ApplicationCommandCreate(s.State.User.ID, "", cmd)
		if err != nil {
			l.Error("cannot create discord command", "command_name", cmd.Name, "error", err)
		}
	}
}

func (g *gameManager) onGuildCreate(s *discordgo.Session, e *discordgo.GuildCreate) {
	l := logger.ComponentLogger("onGuildCreate")
	l.Info("discord guild create event received")
	if e.Guild == nil {
		l.Error("no guild found in guild create event")
		return
	}
	if e.ID != config.Config().Discord.GuildID {
		l.Info("skipping guild create event, guild id mismatch")
		return
	}
	l.Info("guild id match, populating initial information")
	channelID := config.Config().Discord.ChannelID
	func() {
		unlock := g.lockAndUpdateGameState()
		defer unlock()
		g.pool.guildID = e.ID
		g.pool.guildName = e.Name
		channelFound := false
		for _, c := range e.Channels {
			if c.ID != channelID {
				continue
			}
			l.Info("configuring discord channel", "channel_id", c.ID, "channel_name", c.Name)
			g.pool.channelName = c.Name
			g.pool.channelID = c.ID
			channelFound = true
			break
		}
		if !channelFound {
			l.Error("failed to find channel in configuration")
		}
	}()
	l.Info("looking for members already in the voice channel")
	inChannelUserID := map[string]*discordgo.Member{}
	for _, vs := range e.VoiceStates {
		if vs.ChannelID != channelID {
			continue
		}
		inChannelUserID[vs.UserID] = &discordgo.Member{}
		l.Info("found user in voice channel", "user_id", vs.UserID)
	}
	if len(inChannelUserID) == 0 {
		l.Warn("no members found in voice channel")
	}
	for _, m := range e.Members {
		if _, ok := inChannelUserID[m.User.ID]; ok {
			inChannelUserID[m.User.ID] = m
		}
	}
	members := make([]*discordgo.Member, 0, len(inChannelUserID))
	for _, m := range inChannelUserID {
		members = append(members, m)
	}
	g.setPlayersFromMembers(members)
}

// interactionUserID returns the id of the user behind an interaction, or ""
// when there is none. discordgo fills Interaction.Member in a guild and
// Interaction.User only in a DM, so Member.User comes first.
func interactionUserID(ic *discordgo.InteractionCreate) string {
	if ic == nil || ic.Interaction == nil {
		return ""
	}
	if ic.Member != nil && ic.Member.User != nil {
		return ic.Member.User.ID
	}
	if ic.User != nil {
		return ic.User.ID
	}
	return ""
}

func (g *gameManager) onInteractionCreate(s *discordgo.Session, ic *discordgo.InteractionCreate) {
	l := logger.ComponentLogger("onInteractionCreate")
	ctx := context.Background()
	l.Info("received discord interaction", "interaction_type", ic.Type.String())
	if ic.Type == discordgo.InteractionMessageComponent && ic.Message != nil {
		err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})
		if err != nil {
			l.Error(fmt.Sprintf("[onInteractionCreate] - failed to send deferred response : %s", err.Error()))
			return
		}

		mcd := ic.MessageComponentData()
		unlock := g.lockAndUpdateGameState()
		defer unlock()
		lb := g.lobbyByMessage(ic.Message.ID)
		if lb == nil {
			l.Info("no lobby tracks this message", "message_id", ic.Message.ID)
			return
		}
		userID := interactionUserID(ic)
		if lb.seatOf(userID) < 0 {
			l.Info("user not in game, dismissing interaction", "user_id", userID, "lobby_id", lb.id)
			return
		}
		switch mcd.CustomID {
		case "roll_btn":
			l.Info("executing roll action", "lobby_id", lb.id)
			g.rollLocked(ctx, lb)
		case "finish_btn":
			l.Info("executing reset action", "lobby_id", lb.id)
			g.resetLocked(ctx, lb)
		case "cancel_btn":
			l.Info("executing cancel action", "lobby_id", lb.id)
			g.cancelLocked(ctx, lb)
		}
		return
	} else if ic.Type == discordgo.InteractionApplicationCommand {
		err := s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		})
		if err != nil {
			l.Error(fmt.Sprintf("[onInteractionCreate] - failed to send deferred response : %s", err.Error()))
			return
		}
		acd := ic.ApplicationCommandData()
		switch acd.Name {
		case "ldn":
			unlock := g.lockAndUpdateGameState()
			defer unlock()
			// The message is for the lobby the invoking user sits in, else main.
			lb := g.main
			if seated := g.seatIndex()[interactionUserID(ic)]; seated != nil {
				lb = seated
			}
			em, c, err := createDiscordMessage(lb, lb.gs)
			if err != nil {
				l.Error("failed to send deferred response", "error", err)
				return
			}
			msg, err := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
				Content:         &em,
				Components:      &[]discordgo.MessageComponent{c},
				AllowedMentions: noMentions(),
			})
			if err != nil {
				l.Error(fmt.Sprintf("[onInteractionCreate] - failed to send interaction response : %s", err.Error()))
				return
			}
			lb.messageID = &msg.ID
			l.Info("tracking the /ldn message", "lobby_id", lb.id, "message_id", msg.ID)
		}
		return
	}
}

func (g *gameManager) onChannelUpdate(s *discordgo.Session, e *discordgo.ChannelUpdate) {
	l := logger.ComponentLogger("onChannelUpdate")
	l.Info("discord channel update event received")
}

func (g *gameManager) onGuildUpdate(s *discordgo.Session, e *discordgo.GuildUpdate) {
	l := logger.ComponentLogger("onGuildUpdate")
	l.Info("discord guild update event received")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if e.Guild == nil {
		l.Error("no guild found in guild update event")
		return
	}
	if e.Guild.ID != g.pool.guildID {
		l.Info("skipping guild update event, guild id mismatch")
		return
	}
	l.Info("guild id match, populating initial information")
	channelFound := false
	for _, c := range e.Channels {
		if c.ID != config.Config().Discord.ChannelID {
			continue
		}
		l.Info("configuring discord channel", "channel_id", c.ID, "channel_name", c.Name)
		g.pool.channelName = c.Name
		g.pool.channelID = c.ID
		channelFound = true
		break
	}
	if !channelFound {
		l.Error("failed to find channel in configuration")
	}
	g.broadcastAll()
}

// onDiscordVoiceStateUpdate keeps the voice pool in sync with the channel.
// Leaving unseats the player from every lobby without a game in progress.
// Joining seats the player in main only, when main has no game and the player
// sits in no other lobby.
func (g *gameManager) onDiscordVoiceStateUpdate(s *discordgo.Session, u *discordgo.VoiceStateUpdate) {
	l := logger.ComponentLogger("onDiscordVoiceStateUpdate")
	l.Info("voice state update received", "user_id", u.UserID, "channel_id", u.ChannelID)
	unlock := g.lockAndUpdateGameState()
	defer unlock()

	inChannel := u.ChannelID == g.pool.channelID
	wasInChannel := u.BeforeUpdate != nil && u.BeforeUpdate.ChannelID == g.pool.channelID

	var changed []*lobby
	switch {
	case inChannel && wasInChannel:
		// Mute, deafen or stream toggles: nothing changed for the lobbies.
		return

	case !inChannel:
		if !wasInChannel {
			if _, known := g.pool.players[u.UserID]; !known {
				return
			}
		}
		l.Info("player left the voice channel", "user_id", u.UserID)
		for _, lb := range g.lobbies {
			if !lb.gs.GameInProgress && lb.unseat(u.UserID) {
				changed = append(changed, lb)
			}
		}
		delete(g.pool.players, u.UserID)

	default: // joined the channel
		displayName := u.UserID
		if u.Member != nil {
			displayName = u.Member.DisplayName()
		} else if ap, ok := g.pool.players[u.UserID]; ok && ap.Name != nil {
			displayName = *ap.Name
		}
		if err := g.d.Database(context.Background()).Model(&modeldb.Player{}).Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(&modeldb.Player{ID: u.UserID, Name: &displayName}).Error; err != nil {
			l.Warn("failed to update player in database", "user_id", u.UserID, "error", err)
		}
		l.Info("player joined the voice channel", "user_id", u.UserID, "name", displayName)

		stats := g.GetPlayerStats(context.Background(), u.UserID)
		userID := u.UserID
		g.pool.players[userID] = model.AvailablePlayer{
			ID:   &userID,
			Name: &displayName,
			Stats: &model.PlayerStats{
				MaxNumberOfRoll: stats.Stats.MaxNumberOfRoll,
				NumberOfGame:    stats.Stats.NumberOfGame,
				TotalRoll:       stats.Stats.TotalRoll,
			},
		}
		main := g.main
		seated := g.seatIndex()[userID]
		if !main.gs.GameInProgress && (seated == nil || seated == main) {
			slot := main.seatOf(userID)
			if slot < 0 {
				for i := range main.gs.Players {
					if main.gs.Players[i].Player.ID == "" {
						slot = i
						break
					}
				}
			}
			if slot >= 0 {
				l.Info("placing player in lobby", "user_id", userID, "slot", slot, "lobby_id", main.id)
				main.gs.Players[slot].Player = model.DiscordPlayer{ID: userID, Name: &displayName}
				changed = append(changed, main)
			}
		}
	}
	g.touch(changed...)
	g.broadcastAll(changed...)
}
