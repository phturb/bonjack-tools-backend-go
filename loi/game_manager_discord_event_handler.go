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
		g.gs.DiscordGuildID = config.Config().Discord.GuildID
	} else {
		l.Info("configured guild found", "guild_name", guild.Name, "guild_id", guild.ID)
		g.gs.DiscordGuildID = guild.ID
		g.gs.DiscordGuildName = guild.Name

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
		g.gs.DiscordGuildID = e.ID
		g.gs.DiscordGuildName = e.Name
		channelFound := false
		for _, c := range e.Channels {
			if c.ID != channelID {
				continue
			}
			l.Info("configuring discord channel", "channel_id", c.ID, "channel_name", c.Name)
			g.gs.DiscordGuildChannelName = c.Name
			g.gs.DiscordGuildChannelID = c.ID
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
		if g.messageID == nil {
			unlock()
			l.Info("no message currently tracked")
			return
		}
		if ic.Message.ID != *g.messageID {
			unlock()
			l.Info("message id mismatch", "message_id", ic.Message.ID, "tracked_id", *g.messageID)
			return
		}
		found := false
		for _, p := range g.gs.Players {
			if p.Player.ID == ic.User.ID {
				found = true
				break
			}
		}
		unlock()
		if !found {
			l.Info("user not in game, dismissing interaction", "user_id", ic.User.ID)
			return
		}
		switch mcd.CustomID {
		case "roll_btn":
			l.Info("executing roll action")
			g.roll(ctx)
		case "finish_btn":
			l.Info("executing reset action")
			g.reset(ctx)
		case "cancel_btn":
			l.Info("executing cancel action")
			g.cancel(ctx)
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
			em, c, err := createDiscordMessage(g.gs)
			if err != nil {
				l.Error("failed to send deferred response", "error", err)
				return
			}
			msg, err := s.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
				Content:    &em,
				Components: &[]discordgo.MessageComponent{c},
			})
			if err != nil {
				l.Error(fmt.Sprintf("[onInteractionCreate] - failed to send interaction response : %s", err.Error()))
				return
			}
			g.messageID = &msg.ID
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
	if e.Guild.ID != g.gs.DiscordGuildID {
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
		g.gs.DiscordGuildChannelName = c.Name
		g.gs.DiscordGuildChannelID = c.ID
		channelFound = true
		break
	}
	if !channelFound {
		l.Error("failed to find channel in configuration")
	}
	g.broadcastState(nil)
}

func (g *gameManager) onDiscordVoiceStateUpdate(s *discordgo.Session, u *discordgo.VoiceStateUpdate) {
	l := logger.ComponentLogger("onDiscordVoiceStateUpdate")
	l.Info("voice state update received", "user_id", u.UserID, "channel_id", u.ChannelID)
	unlock := g.lockAndUpdateGameState()
	defer unlock()

	inChannel := u.ChannelID == g.gs.DiscordGuildChannelID
	wasInChannel := u.BeforeUpdate != nil && u.BeforeUpdate.ChannelID == g.gs.DiscordGuildChannelID

	switch {
	case inChannel && wasInChannel:
		// Mute, deafen or stream toggles: nothing changed for the lobby.
		return

	case !inChannel:
		if !wasInChannel {
			if _, known := g.gs.AvailablePlayers[u.UserID]; !known {
				return
			}
		}
		l.Info("player left the voice channel", "user_id", u.UserID)
		if !g.gs.GameInProgress {
			for i, p := range g.gs.Players {
				if p.Player.ID == u.UserID {
					g.gs.Players[i] = model.NewEmptyGamePlayer()
				}
			}
		}
		delete(g.gs.AvailablePlayers, u.UserID)

	default: // joined the channel
		displayName := u.UserID
		if u.Member != nil {
			displayName = u.Member.DisplayName()
		} else if ap, ok := g.gs.AvailablePlayers[u.UserID]; ok && ap.Name != nil {
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
		g.gs.AvailablePlayers[userID] = model.AvailablePlayer{
			ID:   &userID,
			Name: &displayName,
			Stats: &model.PlayerStats{
				MaxNumberOfRoll: stats.Stats.MaxNumberOfRoll,
				NumberOfGame:    stats.Stats.NumberOfGame,
				TotalRoll:       stats.Stats.TotalRoll,
			},
		}
		if !g.gs.GameInProgress {
			slot := -1
			for i := range g.gs.Players {
				if g.gs.Players[i].Player.ID == userID {
					slot = i
					break
				}
			}
			if slot < 0 {
				for i := range g.gs.Players {
					if g.gs.Players[i].Player.ID == "" {
						slot = i
						break
					}
				}
			}
			if slot >= 0 {
				l.Info("placing player in lobby", "user_id", userID, "slot", slot)
				g.gs.Players[slot].Player = model.DiscordPlayer{ID: userID, Name: &displayName}
			}
		}
	}
	g.broadcastState(nil)
}
