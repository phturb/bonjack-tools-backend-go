package loi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/phturb/bonjack-tools-backend-go/internal/logger"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/discord"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	sharedmodel "github.com/phturb/bonjack-tools-backend-go/model"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GameManager interface {
	HandleWebsocketMessage(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) bool
	HandleWebsocketConnection(conn *websocket.Conn, r *http.Request)
	StatsController
}

type gameManager struct {
	d internal.Dependencies

	dm discord.DiscordManager

	messageID *string

	connsMu sync.RWMutex
	conns   []*websocket.Conn

	gsMu sync.RWMutex
	gs   *model.GameState
}

var _ GameManager = (*gameManager)(nil)

func NewGameManager(d internal.Dependencies, dm discord.DiscordManager) GameManager {
	gs := model.NewDefaultGameState()
	gm := &gameManager{
		d:       d,
		dm:      dm,
		connsMu: sync.RWMutex{},
		conns:   []*websocket.Conn{},
		gsMu:    sync.RWMutex{},
		gs:      &gs,
	}
	dm.Session().AddHandler(gm.onDiscordReady)
	dm.Session().AddHandler(gm.onGuildCreate)
	dm.Session().AddHandler(gm.onGuildUpdate)
	dm.Session().AddHandler(gm.onDiscordVoiceStateUpdate)
	dm.Session().AddHandler(gm.onChannelUpdate)
	dm.Session().AddHandler(gm.onInteractionCreate)

	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				func() {
					unlock := gm.lockAndUpdateGameState()
					defer unlock()
					gm.gs.NextRollTimer = gm.gs.NextRollTimer - 5000
					if gm.gs.NextRollTimer <= 0 {
						gm.gs.NextRollTimer = 0
						if !gm.gs.CanRoll {
							gm.gs.CanRoll = true
							gm.broadcastState(nil)
						}
					}
				}()
			}
		}
	}()
	return gm
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
		if g.ID == internal.Config().Discord.GuildID {
			guild = g
			break
		}
	}
	if guild == nil {
		l.Error("no guild found in discord ready event")
		panic("no guild found in discord ready event")
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
		if c.ID != internal.Config().Discord.ChannelID {
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
		if c.ID != internal.Config().Discord.ChannelID {
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

func updateDiscordMessage(messageID string, s *discordgo.Session, gs *model.GameState) error {
	em, c, err := createDiscordMessage(gs)
	if err != nil {
		return err
	}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel: internal.Config().Discord.ChannelID,
		ID:      messageID,
		Content: &em,
		Components: &[]discordgo.MessageComponent{
			c,
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func createDiscordMessage(gs *model.GameState) (string, discordgo.MessageComponent, error) {
	if gs == nil {
		return "", nil, errors.New("game state is nil")
	}
	description := ""
	if gs.GameInProgress {
		description = fmt.Sprintf("Loi in progress, roll number : %d", gs.RollCount)
	} else {
		description = "Waiting for players ...\nGo see the web dashboard at https://tools.bonjack.club/lois-des-norms"
	}
	description += "\n"
	for i, p := range gs.Players {
		name := ""
		if p.Player.Name == nil || *p.Player.Name == "" {
			name = "Empty Spot"
		} else {
			name = *p.Player.Name
		}
		position := fmt.Sprintf("*Player %d*", i+1)
		if p.Role != nil && p.Champion != nil {
			position = fmt.Sprintf("*%s - %s*", p.Champion.Name, *p.Role)
		}
		description += fmt.Sprintf("> %s\t%s\n", position, name)
	}

	return description, discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.Button{
				CustomID: "roll_btn",
				Label:    "Roll",
				Style:    discordgo.PrimaryButton,
				Disabled: !gs.CanRoll,
			},
			discordgo.Button{
				CustomID: "finish_btn",
				Label:    "Finish",
				Style:    discordgo.SuccessButton,
				Disabled: !gs.GameInProgress,
			},
			discordgo.Button{
				CustomID: "cancel_btn",
				Label:    "Cancel",
				Style:    discordgo.DangerButton,
			},
		},
	}, nil
}

func (g *gameManager) setPlayersFromMembers(members []*discordgo.Member) {
	l := logger.ComponentLogger("setPlayersFromMembers")
	ctx := context.Background()
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	aps := make(map[string]model.AvailablePlayer)
	for _, m := range members {
		if m == nil {
			l.Warn("[playersFromMembers] - discord api returned empty members for discord channel")
			continue
		}
		if m.User == nil || m.User.ID == "" {
			l.Warn("[playersFromMembers] - member is missing user, can't identify user id")
			continue
		}
		name := m.DisplayName()
		stats := g.GetPlayerStats(ctx, m.User.ID)
		ap := model.AvailablePlayer{
			ID:   &m.User.ID,
			Name: &name,
			Stats: &model.PlayerStats{
				MaxNumberOfRoll: stats.Stats.MaxNumberOfRoll,
				NumberOfGame:    stats.Stats.NumberOfGame,
				TotalRoll:       stats.Stats.TotalRoll,
			},
		}

		aps[*ap.ID] = ap
		if err := g.d.Database(ctx).Model(&sharedmodel.Player{}).Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(&sharedmodel.Player{
			ID:   *ap.ID,
			Name: ap.Name,
		}).Error; err != nil {
			l.Warn("[playersFromMembers] - failed to update player in database : " + err.Error())
			continue
		}
	}

	g.gs.AvailablePlayers = aps
	if !g.gs.GameInProgress {
		for i, p := range g.gs.Players {
			if p.Player.ID == "" {
				continue
			}
			if ap, ok := aps[p.Player.ID]; !ok {
				l.Warn(fmt.Sprintf("[playersFromMembers] - player not found '%s' in game state", p.Player.ID))
				g.gs.Players[i] = model.NewEmptyGamePlayer()
			} else {
				g.gs.Players[i].Player.ID = *ap.ID
				g.gs.Players[i].Player.Name = ap.Name
			}
		}
		for len(g.gs.Players) < 5 {
			g.gs.Players = append(g.gs.Players, model.NewEmptyGamePlayer())
		}
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

// HandleWebsocketConnection implements GameManager.
func (g *gameManager) HandleWebsocketConnection(conn *websocket.Conn, r *http.Request) {
	l := logger.ComponentLogger("HandleWebsocketConnection")
	if r == nil {
		l.Info("handling websocket disconnection")
		g.connsMu.Lock()
		defer g.connsMu.Unlock()
		newConns := make([]*websocket.Conn, 0, len(g.conns))
		for _, c := range g.conns {
			if c == conn {
				continue
			}
			newConns = append(g.conns, c)
		}
		g.conns = newConns
		return
	}
	l.Info("handling websocket connection")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	sgs, err := json.Marshal(*g.gs)
	if err != nil {
		l.Error(fmt.Sprintf("failed to serialized game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	g.connsMu.Lock()
	defer g.connsMu.Unlock()
	g.conns = append(g.conns, conn)
	l.Info("[HandleWebsocketConnection] - sending game state to the new connection")
	if err := conn.WriteJSON(m); err != nil {
		l.Error(err.Error())
	}
}

func (g *gameManager) broadcast(m interface{}, sender *websocket.Conn) error {
	l := logger.ComponentLogger("broadcast")
	l.Info("broadcasting message")
	g.connsMu.RLock()
	defer g.connsMu.RUnlock()
	var errs []error
	for _, c := range g.conns {
		if c == sender {
			continue
		}
		if err := c.WriteJSON(m); err != nil {
			errs = append(errs, err)
		}
	}
	err := errors.Join(errs...)
	if err != nil {
		l.WithError(err).Error("[broadcast] - failed to broadcast message")
	} else {
		l.Info("message broadcasted successfully")
	}
	return err
}

// HandleWebsocket implements GameManager.
func (g *gameManager) HandleWebsocketMessage(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) bool {
	l := logger.ComponentLogger("HandleWebsocketMessage")
	l.Info(fmt.Sprintf("%s event received", wm.Action))
	switch wm.Action {
	case modelwebsocket.UpdatePlayers:
		go g.handleUpdatePlayers(wm, conn, r)
		return true
	case modelwebsocket.Roll:
		go g.handleRoll(wm, conn, r)
		return true
	case modelwebsocket.Cancel:
		go g.handleCancel(wm, conn, r)
		return true
	case modelwebsocket.Reset:
		go g.handleReset(wm, conn, r)
		return true
	case modelwebsocket.RefreshDiscord:
		go g.handleRefreshDiscord(wm, conn, r)
		return true
	default:
		l.Debug(fmt.Sprintf("websocket action '%s' is not handled by the game manager", wm.Action))
		return false
	}
}

func (g *gameManager) broadcastState(sender *websocket.Conn) {
	l := logger.ComponentLogger("broadcastState")
	d, err := json.Marshal(g.gs)
	if err != nil {
		l.Error(fmt.Sprintf("failed to marshal game state : %s", err.Error()))
		return
	}
	var gs model.GameState
	err = json.Unmarshal(d, &gs)
	if err != nil {
		l.Error(fmt.Sprintf("failed to unmarshal game state : %s", err.Error()))
		return
	}
	if gs.GameInProgress {
		l.Info(fmt.Sprintf("game in progress, making sure the in game players are in the available players"))
		ap := map[string]model.AvailablePlayer{}
		maps.Copy(ap, g.gs.AvailablePlayers)
		gs.AvailablePlayers = ap
		for _, p := range gs.Players {
			if p.Player.ID == "" {
				continue
			}
			if _, ok := gs.AvailablePlayers[p.Player.ID]; !ok {
				gs.AvailablePlayers[p.Player.ID] = model.AvailablePlayer{
					ID:   &p.Player.ID,
					Name: p.Player.Name,
				}
			}
		}
	}
	sgs, err := json.Marshal(gs)
	if err != nil {
		l.Error(fmt.Sprintf("failed to marshal game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	if sender != nil {
		if err := sender.WriteJSON(m); err != nil {
			l.Error(fmt.Sprintf("failed to broadcast game state : %s", err.Error()))
		}
	} else {
		if err := g.broadcast(m, nil); err != nil {
			l.Error(fmt.Sprintf("failed to broadcast game state : %s", err.Error()))
		}
	}
	if g.messageID != nil {
		if err := updateDiscordMessage(*g.messageID, g.dm.Session(), &gs); err != nil {
			l.Error(fmt.Sprintf("failed to update discord message on broadcast: %s", err.Error()))
		}
	}
}

func (g *gameManager) handleUpdatePlayers(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	l := logger.ComponentLogger("handleUpdatePlayers")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if g.gs.GameInProgress {
		l.Warn("[handleUpdatePlayers] - game is in progress, skipping update")
		g.broadcastState(conn)
		return
	}
	l.Warn("[handleUpdatePlayers] - game is not in progress updating players list")

	type content struct {
		ID   string  `json:"id"`
		Name *string `json:"name,omitempty"`
	}
	var cs []content
	if err := json.Unmarshal([]byte(wm.Content), &cs); err != nil {
		l.Error(fmt.Sprintf("[handleUpdatePlayers] - failed to unmarshal content : %s", err.Error()))
		return
	}

	var gps []model.GamePlayer
	naps := map[string]content{}
	for _, c := range cs {
		if len(gps) >= 5 {
			l.Warn("[handleUpdatePlayers] dropping any players going beyond 5")
			break
		}
		if c.ID != "" {
			if _, ok := naps[c.ID]; ok {
				l.Warn(fmt.Sprintf("[handleUpdatePlayers] - double entry for player %s, dropping entry", c.ID))
				continue
			}
		}
		if c.ID == "" {
			gps = append(gps, model.NewEmptyGamePlayer())
			continue
		}
		naps[c.ID] = c
		if ap, ok := g.gs.AvailablePlayers[c.ID]; ok {
			gps = append(gps, model.GamePlayer{
				Player: model.DiscordPlayer{
					ID:   c.ID,
					Name: ap.Name,
				},
			})
		} else {
			gps = append(gps, model.NewEmptyGamePlayer())
		}
	}
	for len(gps) < 5 {
		l.Warn("[handleUpdatePlayers] missing player, adding empty one")
		gps = append(gps, model.NewEmptyGamePlayer())
	}
	g.gs.Players = gps
	g.broadcastState(nil)
}

func (g *gameManager) handleRoll(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	g.roll(r.Context())
}

func (g *gameManager) roll(ctx context.Context) {
	l := logger.ComponentLogger("roll")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if !g.gs.CanRoll {
		l.Info("game is not allowed to roll")
		return
	}
	db := g.d.Database(ctx)
	var lVer sharedmodel.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		l.Error(fmt.Sprintf("failed to retrieve version : %s", err.Error()))
		return
	}
	g.gs.LeagueVersion = lVer.Version
	g.gs.CanRoll = false
	g.gs.NextRollTimer = internal.Config().GameManager.TimerTime
	if !g.gs.GameInProgress {
		l.Info("game is not in progress, updating database with initial roll")
		game := sharedmodel.Game{}
		db.Transaction(func(tx *gorm.DB) error {
			// SQLite doesn't support constraint deferral, so skip for SQLite
			if tx.Dialector.Name() != "sqlite" {
				if err := tx.Exec("SET CONSTRAINTS ALL DEFERRED;").Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&sharedmodel.Game{}).Create(&game).Error; err != nil {
				return err
			}
			gps := make([]sharedmodel.GamePlayer, 0)
			for _, p := range g.gs.Players {
				if p.Player.ID == "" {
					continue
				}
				gps = append(gps, sharedmodel.GamePlayer{
					GameID:   game.ID,
					PlayerID: p.Player.ID,
				})
			}

			if err := tx.Model(&sharedmodel.GamePlayer{}).Create(gps).Error; err != nil {
				return err
			}
			return nil
		})
		g.gs.GameID = game.ID
		l.Info("loi des norms has started")
	}
	g.gs.RollCount += 1
	l.Info(fmt.Sprintf("incrementing roll count to %d", g.gs.RollCount))

	gprs := make([]sharedmodel.GamePlayerRoll, 0)
	rcs := make([]sharedmodel.Champion, 0, 5)
	l.Info("shuffling the new roles")
	roles := model.NewRoleSlice().Shuffle()
	for i, p := range g.gs.Players {
		l.Info(fmt.Sprintf("assigning player %s the role %s", p.Player.ID, roles[i]))
		g.gs.Players[i].Role = &roles[i]
		if p.Player.ID == "" {
			continue
		}
		var rc sharedmodel.Champion
		l.Info(fmt.Sprintf("retrieving the player %s list of champions", p.Player.ID))
		pcs, _ := g.retrieveChampionsForPlayer(ctx, p)
		if len(pcs) <= 0 {
			l.Error("no champion to select from")
			return
		}
		for len(pcs) > 0 {
			ri := rand.Intn(len(pcs))
			trc := pcs[ri]
			contain := false
			for _, c := range rcs {
				if trc.ID == c.ID {
					contain = true
					pcs[ri] = pcs[len(pcs)-1]
					pcs = pcs[:len(pcs)-1]
					break
				}
			}
			if !contain {
				rc = trc
				break
			}
		}
		l.Info(fmt.Sprintf("assigning player %s the champion %s", p.Player.ID, rc.Name))
		rcs = append(rcs, rc)
		g.gs.Players[i].Champion = model.ChampionFromDB(&rc)
		gprs = append(gprs, sharedmodel.GamePlayerRoll{
			GameID:     g.gs.GameID,
			PlayerID:   p.Player.ID,
			RollNumber: g.gs.RollCount,
			Role:       string(roles[i]),
			ChampionID: rc.ID,
		})
	}

	l.Info(fmt.Sprintf("updating the database with the current rolls"))
	if err := db.Model(&sharedmodel.GamePlayerRoll{}).Create(&gprs).Error; err != nil {
		l.Error("failed to update database with game player roll")
		for i, p := range g.gs.Players {
			l.Info(fmt.Sprintf("removing player %s role", p.Player.ID))
			g.gs.Players[i].Role = nil
			g.gs.Players[i].Champion = nil
			if p.Player.ID == "" {
				continue
			}
		}
		g.gs.RollCount -= 1
		return
	}

	if !g.gs.GameInProgress {
		l.Info(fmt.Sprintf("loi des norms (%d) has started", g.gs.GameID))
		g.gs.GameInProgress = true
	}

	l.Info(fmt.Sprintf("sending the new game state to the users"))
	g.broadcastState(nil)
}

func (g *gameManager) retrieveChampionsForPlayer(ctx context.Context, gp model.GamePlayer) ([]sharedmodel.Champion, error) {
	l := logger.ComponentLogger("retrieveChampionsForPlayer")
	cs := make([]sharedmodel.Champion, 0)
	err := g.d.Database(ctx).Transaction(func(tx *gorm.DB) error {
		var p sharedmodel.Player
		if err := tx.Model(&sharedmodel.Player{}).Preload("Champions").Where("id = ?", gp.Player.ID).Find(&p).Error; err != nil {
			l.Error(fmt.Sprintf("failed to retrieve player champions : %s", err.Error()))
			return err
		}
		cs = append(cs, p.Champions...)
		if len(cs) <= 0 {
			if err := tx.Model(&sharedmodel.Champion{}).Find(&cs).Error; err != nil {
				l.Error(fmt.Sprintf("failed to retrieve all champions : %s", err.Error()))
				return err
			}
		}
		wcs := make([]sharedmodel.WeeklyChampion, 0)
		if err := tx.Model(&sharedmodel.WeeklyChampion{}).Preload("Champion").Find(&wcs).Error; err != nil {
			l.Error(fmt.Sprintf("failed to retrieve weekly champions : %s", err.Error()))
			return err
		}
		for _, wc := range wcs {
			include := false
			for _, c := range cs {
				if c.ID == wc.Champion.ID {
					include = true
					break
				}
			}
			if !include {
				cs = append(cs, wc.Champion)
			}
		}
		return nil
	})
	return cs, err
}

func (g *gameManager) handleCancel(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	g.cancel(r.Context())
}

func (g *gameManager) cancel(ctx context.Context) {
	l := logger.ComponentLogger("cancel")
	db := g.d.Database(ctx)
	if err := func() error {
		unlock := g.lockAndUpdateGameState()
		defer unlock()
		if g.gs.GameInProgress && g.gs.RollCount > 0 {
			l.Info(fmt.Sprintf("cancelling the current loi"))
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("game_id = ?", g.gs.GameID).Delete(&sharedmodel.GamePlayerRoll{}).Error; err != nil {
					return err
				}
				if err := tx.Where("game_id = ?", g.gs.GameID).Delete(&sharedmodel.GamePlayer{}).Error; err != nil {
					return err
				}
				if err := tx.Where("id = ?", g.gs.GameID).Delete(&sharedmodel.Game{}).Error; err != nil {
					return err
				}
				return nil
			})
			if err != nil {
				l.Error("[handleCancel] - " + err.Error())
				return err
			}
			g.gs.GameID = 0
		}
		return nil
	}(); err != nil {
		return
	}
	g.reset(ctx)
}

func (g *gameManager) handleReset(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	g.reset(r.Context())
}

func (g *gameManager) reset(ctx context.Context) {
	l := logger.ComponentLogger("reset")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	db := g.d.Database(ctx)
	var lVer sharedmodel.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		return
	}
	l.Info(fmt.Sprintf("[handleReset] - resetting the game state values"))
	content := fmt.Sprintf("Loi is finished or canceled after %d roll", g.gs.RollCount)
	g.gs.LeagueVersion = lVer.Version
	g.gs.GameInProgress = false
	g.gs.RollCount = 0
	g.gs.NextRollTimer = 0
	g.gs.CanRoll = true
	if g.messageID != nil {
		_, err := g.dm.Session().ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel: internal.Config().Discord.ChannelID,
			ID:      *g.messageID,
			Content: &content,
		})
		if err != nil {
			l.Error(fmt.Sprintf("[handleReset] - faile to update channel message : %s", err.Error()))
		}
	}
	g.messageID = nil
	l.Info(fmt.Sprintf("[handleReset] - removing players that are no longer available"))
	for i := range g.gs.Players {
		if _, ok := g.gs.AvailablePlayers[g.gs.Players[i].Player.ID]; !ok {
			g.gs.Players[i] = model.NewEmptyGamePlayer()
		} else {
			g.gs.Players[i].Role = nil
			g.gs.Players[i].Champion = nil
		}
	}
	g.broadcastState(nil)
}

func (g *gameManager) handleRefreshDiscord(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
}

func (g *gameManager) lockAndUpdateGameState() func() {
	g.gsMu.Lock()
	return g.gsMu.Unlock
}
