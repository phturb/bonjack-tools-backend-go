package loi

import (
	"context"
	"fmt"

	"github.com/phturb/bonjack-tools-backend-go/internal/logger"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/loi/internal/model"
	sharedmodel "github.com/phturb/bonjack-tools-backend-go/model"
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
		return
	}
	l.Info("configured guild found", "guild_name", guild.Name, "guild_id", guild.ID)
	g.gs.DiscordGuildID = guild.ID
	g.gs.DiscordGuildName = guild.Name
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
	if e.ID != g.gs.DiscordGuildID {
		l.Info("skipping guild create event, guild id mismatch")
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
	l.Info("looking for members already in the voice channel")
	inChannelUserID := map[string]*discordgo.Member{}
	for _, vs := range e.VoiceStates {
		if vs.ChannelID != g.gs.DiscordGuildChannelID {
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
	l.Info("[onDiscordVoiceStateUpdate] - event received")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if u.ChannelID != g.gs.DiscordGuildChannelID {
		l.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - channel ID is not the supported one '%s', assuming player quit the channel", u.ChannelID))
		if !g.gs.GameInProgress {
			for i, p := range g.gs.Players {
				if p.Player.ID == u.Member.User.ID {
					g.gs.Players[i] = model.NewEmptyGamePlayer()
					l.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - removing player id %s from game state", p.Player.ID))
				}
			}
			l.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - removing player with id %s and name %s from available players", u.Member.User.ID, u.Member.DisplayName()))
			delete(g.gs.AvailablePlayers, u.Member.User.ID)
		} else {
			l.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - removing player with id %s and name %s from available players", u.Member.User.ID, u.Member.DisplayName()))
			delete(g.gs.AvailablePlayers, u.Member.User.ID)
		}
	} else {
		displayName := u.Member.DisplayName()

		if err := g.d.Database(context.Background()).Model(&sharedmodel.Player{}).Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(&sharedmodel.Player{
			ID:   u.UserID,
			Name: &displayName,
		}).Error; err != nil {
			l.Warn("[onDiscordVoiceStateUpdate] - failed to update player in database : " + err.Error())
		}

		l.Info(fmt.Sprintf("[onDiscordVoiceStateUpdate] - adding player with id %s and name %s to available players", u.Member.User.ID, displayName))

		stats := g.GetPlayerStats(context.Background(), u.Member.User.ID)
		ap := model.AvailablePlayer{
			ID:   &u.Member.User.ID,
			Name: &displayName,
			Stats: &model.PlayerStats{
				MaxNumberOfRoll: stats.Stats.MaxNumberOfRoll,
				NumberOfGame:    stats.Stats.NumberOfGame,
				TotalRoll:       stats.Stats.TotalRoll,
			},
		}
		g.gs.AvailablePlayers[u.Member.User.ID] = ap
		if !g.gs.GameInProgress {
			found := false
			for i := range g.gs.Players {
				if g.gs.Players[i].Player.ID == u.Member.User.ID {
					l.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - adding player with id %s and name %s to current players slot %d", u.Member.User.ID, displayName, i))
					g.gs.Players[i].Player = model.DiscordPlayer{
						ID:   u.Member.User.ID,
						Name: &displayName,
					}
					found = true
					break
				}
			}
			if !found {
				for i := range g.gs.Players {
					if g.gs.Players[i].Player.ID == "" {
						l.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - adding player with id %s and name %s to current players slot %d", u.Member.User.ID, displayName, i))
						g.gs.Players[i].Player = model.DiscordPlayer{
							ID:   u.Member.User.ID,
							Name: &displayName,
						}
						break
					}
				}
			}
		}
	}
	g.broadcastState(nil)
}
