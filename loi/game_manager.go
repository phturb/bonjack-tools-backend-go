package loi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

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
}

type gameManager struct {
	d internal.Dependencies

	dm discord.DiscordManager

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

	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				func() {
					gm.gsMu.Lock()
					defer gm.gsMu.Unlock()
					gm.gs.NextRollTimer = gm.gs.NextRollTimer - 1000
					if gm.gs.NextRollTimer < 0 {
						gm.gs.NextRollTimer = 0
					}
				}()
			}
		}
	}()
	return gm
}

func (g *gameManager) onDiscordReady(s *discordgo.Session, e *discordgo.Ready) {
	slog.Info("[onDiscordReady] - event received")
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
		slog.Error("[onDiscordReady] - onDiscordReady - no guild found")
		panic("[onDiscordReady] - onDiscordReady - no guild found")
	}
	slog.Info(fmt.Sprintf("[onDiscordReady] - configured guild found %s (%s)", guild.Name, guild.ID))
	g.gs.DiscordGuildID = guild.ID
	g.gs.DiscordGuildName = guild.Name
}

func (g *gameManager) onChannelUpdate(s *discordgo.Session, e *discordgo.ChannelUpdate) {
	slog.Info("[onChannelUpdate] - event received")
}

func (g *gameManager) onGuildUpdate(s *discordgo.Session, e *discordgo.GuildUpdate) {
	slog.Info("[onGuildUpdate] - event received")
	if e.Guild == nil {
		slog.Error("[onGuildUpdate] - no guild found")
		return
	}
	if e.Guild.ID != g.gs.DiscordGuildID {
		slog.Info("[onGuildUpdate] - skipping onGuildUpdate event, guild id mismatch")
		return
	}
	slog.Info("[onGuildUpdate] - guild id match, populating initial information")
	channelFound := false
	for _, c := range e.Channels {
		if c.ID != internal.Config().Discord.ChannelID {
			continue
		}
		slog.Info(fmt.Sprintf("[onGuildUpdate] - configuring channel id %s with name %s", c.ID, c.Name))
		g.gs.DiscordGuildChannelName = c.Name
		g.gs.DiscordGuildChannelID = c.ID
		channelFound = true
		break
	}
	if !channelFound {
		slog.Error("[onGuildUpdate] - failed to find channel in config")
	}

	sgs, err := json.Marshal(*g.gs)
	if err != nil {
		slog.Error(fmt.Sprintf("[onGuildUpdate] - failed to serialized game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	g.broadcast(m, nil)
}

func (g *gameManager) onGuildCreate(s *discordgo.Session, e *discordgo.GuildCreate) {
	slog.Info("[onGuildCreate] - event received")
	if e.Guild == nil {
		slog.Error("[onGuildCreate] - no guild found")
		return
	}
	if e.Guild.ID != g.gs.DiscordGuildID {
		slog.Info("[onGuildCreate] - skipping onGuildCreate event, guild id mismatch")
		return
	}
	slog.Info("[onGuildCreate] - guild id match, populating initial information")
	channelFound := false
	for _, c := range e.Channels {
		if c.ID != internal.Config().Discord.ChannelID {
			continue
		}
		slog.Info(fmt.Sprintf("[onGuildCreate] - configuring channel id %s with name %s", c.ID, c.Name))
		g.gs.DiscordGuildChannelName = c.Name
		g.gs.DiscordGuildChannelID = c.ID
		channelFound = true
		break
	}
	if !channelFound {
		slog.Error("[onGuildCreate] - failed to find channel in config")
	}
	slog.Info("[onGuildCreate] - looking for members already in the voice channel")
	inChannelUserID := map[string]*discordgo.Member{}
	for _, vs := range e.VoiceStates {
		if vs.ChannelID != g.gs.DiscordGuildChannelID {
			continue
		}
		inChannelUserID[vs.UserID] = &discordgo.Member{}
		slog.Info(fmt.Sprintf("[onGuildCreate] - found user id '%s' in voice channel", vs.UserID))
	}
	if len(inChannelUserID) == 0 {
		slog.Warn("[onGuildCreate] - no members found in voice channel")
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

func (g *gameManager) setPlayersFromMembers(members []*discordgo.Member) {
	ctx := context.Background()
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	aps := make(map[string]model.AvailablePlayer)
	for _, m := range members {
		var ap model.AvailablePlayer
		if m == nil {
			slog.Warn("discord api returned empty members for discord channel")
			continue
		}
		if m.User == nil || m.User.ID == "" {
			slog.Warn("member is missing user, can't identify user id")
			continue
		}
		name := m.Nick
		if name == "" {
			slog.Warn(fmt.Sprintf("user id '%s' is missing nick name, using display name '%s'", m.User.ID, m.Nick))
			name = m.DisplayName()
		}
		ap = model.AvailablePlayer{
			ID:   &m.User.ID,
			Name: &name,
		}
		aps[*ap.ID] = ap
		if err := g.d.Database(ctx).Model(&sharedmodel.Player{}).Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(&sharedmodel.Player{
			ID:   *ap.ID,
			Name: ap.Name,
		}).Error; err != nil {
			slog.Warn("failed to update player in database : " + err.Error())
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
				slog.Warn(fmt.Sprintf("player not found '%s' in game state", p.Player.ID))
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

	sgs, err := json.Marshal(*g.gs)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to serialized game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	g.broadcast(m, nil)
}

func (g *gameManager) onDiscordVoiceStateUpdate(s *discordgo.Session, u *discordgo.VoiceStateUpdate) {
	slog.Info("[onDiscordVoiceStateUpdate] - event received")
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	if u.ChannelID != g.gs.DiscordGuildChannelID {
		slog.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - channel ID is not the supported one '%s', assuming player quit the channel", u.ChannelID))
		if !g.gs.GameInProgress {
			for i, p := range g.gs.Players {
				if p.Player.ID == u.Member.User.ID {
					g.gs.Players[i] = model.NewEmptyGamePlayer()
					slog.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - removing player id %s from game state", p.Player.ID))
				}
			}
		}
		slog.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - removing player with id %s and name %s from available players", u.Member.User.ID, u.Member.DisplayName()))
		delete(g.gs.AvailablePlayers, u.Member.User.ID)
	} else {
		displayName := u.Member.DisplayName()
		slog.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - adding player with id %s and name %s to available players", u.Member.User.ID, displayName))
		g.gs.AvailablePlayers[u.Member.User.ID] = model.AvailablePlayer{
			ID:   &u.Member.User.ID,
			Name: &displayName,
		}
		if !g.gs.GameInProgress {
			for i := range g.gs.Players {
				if g.gs.Players[i].Player.ID == "" {
					slog.Warn(fmt.Sprintf("[onDiscordVoiceStateUpdate] - adding player with id %s and name %s to current players slot %d", u.Member.User.ID, displayName, i))
					g.gs.Players[i].Player = model.DiscordPlayer{
						ID:   u.Member.User.ID,
						Name: &displayName,
					}
					break
				}
			}
		}
	}

	sgs, err := json.Marshal(*g.gs)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to serialized game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	g.broadcast(m, nil)
}

// HandleWebsocketConnection implements GameManager.
func (g *gameManager) HandleWebsocketConnection(conn *websocket.Conn, r *http.Request) {
	slog.Info("[HandleWebsocketConnection] - handling websocket connection")
	g.gsMu.RLock()
	defer g.gsMu.RUnlock()
	sgs, err := json.Marshal(*g.gs)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to serialized game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	g.connsMu.Lock()
	defer g.connsMu.Unlock()
	g.conns = append(g.conns, conn)
	slog.Info("[HandleWebsocketConnection] - sending game state to the new connection")
	if err := conn.WriteJSON(m); err != nil {
		slog.Error(err.Error())
	}
}

func (g *gameManager) broadcast(m interface{}, sender *websocket.Conn) error {
	slog.Info("[broadcast] - broadcasting message")
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
	return errors.Join(errs...)
}

// HandleWebsocket implements GameManager.
func (g *gameManager) HandleWebsocketMessage(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) bool {
	slog.Info(fmt.Sprintf("[HandleWebsocketMessage] - %s event received", wm.Action))
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
		slog.Debug(fmt.Sprintf("websocket action '%s' is not handled by the game manager", wm.Action))
		return false
	}
}

func (g *gameManager) handleUpdatePlayers(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	if g.gs.GameInProgress {
		slog.Warn("[handleUpdatePlayers] - game is in progress, skipping update")
		sgs, err := json.Marshal(*g.gs)
		if err != nil {
			slog.Error(fmt.Sprintf("[handleUpdatePlayers] - failed to marshal game state : %s", err.Error()))
			return
		}
		m := modelwebsocket.Message{
			Action:  modelwebsocket.UpdateState,
			Content: string(sgs),
		}
		conn.WriteJSON(m)
		return
	}
	slog.Warn("[handleUpdatePlayers] - game is not in progress updating players list")

	type content struct {
		ID   string  `json:"id"`
		Name *string `json:"name,omitempty"`
	}
	var cs []content
	if err := json.Unmarshal([]byte(wm.Content), &cs); err != nil {
		slog.Error(err.Error())
		return
	}

	var gps []model.GamePlayer
	naps := map[string]content{}
	j := 0
	for _, c := range cs {
		if c.ID != "" {
			if _, ok := naps[c.ID]; ok {
				slog.Warn(fmt.Sprintf("[handleUpdatePlayers] - double entry for player %s, dropping entry", c.ID))
				continue
			}
		}
		j++
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
		if j > 4 {
			slog.Warn("[handleUpdatePlayers] dropping any players going beyond 5")
			break
		}
	}
	for len(gps) < 5 {
		slog.Warn("[handleUpdatePlayers] missing player, adding empty one")
		gps = append(gps, model.NewEmptyGamePlayer())
	}
	g.gs.Players = gps

	sgs, err := json.Marshal(*g.gs)
	if err != nil {
		slog.Error(fmt.Sprintf("[handleUpdatePlayers] - failed to marshal game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	g.broadcast(m, nil)
}

func (g *gameManager) handleRoll(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	ctx := r.Context()
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	if !g.gs.CanRoll {
		slog.Info("[handleRoll] - game is not allowed to roll")
		return
	}
	db := g.d.Database(r.Context())
	var lVer sharedmodel.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		return
	}
	g.gs.LeagueVersion = lVer.Version
	g.gs.CanRoll = false
	g.gs.NextRollTimer = internal.Config().GameManager.TimerTime
	if !g.gs.GameInProgress {
		slog.Info("[handleRoll] - game is not in progress, updating database with initial roll")
		game := sharedmodel.Game{}
		db.Transaction(func(tx *gorm.DB) error {
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
		g.gs.GameId = game.ID
		slog.Info("loi des norms has started")
	}
	g.gs.RollCount += 1
	slog.Info(fmt.Sprintf("[handleRoll] - incrementing roll count to %d", g.gs.RollCount))

	gprs := make([]sharedmodel.GamePlayerRoll, 0)
	rcs := make([]sharedmodel.Champion, 0, 5)
	slog.Info("[handleRoll] - shuffling the new roles")
	roles := model.NewRoleSlice().Shuffle()
	for i, p := range g.gs.Players {
		slog.Info(fmt.Sprintf("[handleRoll] - assigning player %s the role %s", p.Player.ID, roles[i]))
		g.gs.Players[i].Role = &roles[i]
		if p.Player.ID == "" {
			continue
		}
		var rc sharedmodel.Champion
		slog.Info(fmt.Sprintf("[handleRoll] - retrieving the player %s list of champions", p.Player.ID))
		pcs, _ := g.retrieveChampionsForPlayer(ctx, p)
		if len(pcs) <= 0 {
			slog.Error("no champion to select from")
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
		slog.Info(fmt.Sprintf("[handleRoll] - assigning player %s the champion %s", p.Player.ID, rc.Name))
		rcs = append(rcs, rc)
		g.gs.Players[i].Champion = model.ChampionFromDB(&rc)
		gprs = append(gprs, sharedmodel.GamePlayerRoll{
			GameID:     g.gs.GameId,
			PlayerID:   p.Player.ID,
			RollNumber: g.gs.RollCount,
			Role:       (&roles[i]).StringPtr(),
			ChampionID: &rc.ID,
		})
	}

	slog.Info(fmt.Sprintf("[handleRoll] - updating the database with the current rolls"))
	if err := db.Model(&sharedmodel.GamePlayerRoll{}).Create(&gprs).Error; err != nil {
		slog.Error("failed to update database with game player roll")
		return
	}

	if !g.gs.GameInProgress {
		slog.Info(fmt.Sprintf("[handleRoll] - loi des norms (%d) has started", g.gs.GameId))
		g.gs.GameInProgress = true
	}

	slog.Info(fmt.Sprintf("[handleRoll] - sending the new game state to the users"))
	sgs, err := json.Marshal(*g.gs)
	if err != nil {
		slog.Error(fmt.Sprintf("failed to marshal game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	g.broadcast(m, nil)
}

func (g *gameManager) retrieveChampionsForPlayer(ctx context.Context, gp model.GamePlayer) ([]sharedmodel.Champion, error) {
	db := g.d.Database(ctx)
	cs := make([]sharedmodel.Champion, 0)
	err := g.d.Database(ctx).Transaction(func(tx *gorm.DB) error {
		pcs := make([]sharedmodel.PlayerChampion, 0)
		if err := db.Model(&sharedmodel.PlayerChampion{}).Preload("Champion").Find(&pcs, "player_id = ?", gp.Player.ID).Error; err != nil {
			slog.Error(fmt.Sprintf("failed to retrieve player champions : %s", err.Error()))
			return err
		}
		for _, pc := range pcs {
			if pc.Champion != nil {
				cs = append(cs, *pc.Champion)
			}
		}
		if len(cs) <= 0 {
			if err := db.Model(&sharedmodel.Champion{}).Find(&cs).Error; err != nil {
				slog.Error(fmt.Sprintf("failed to retrieve all champions : %s", err.Error()))
				return err
			}
		}
		wcs := make([]sharedmodel.WeeklyChampion, 0)
		if err := db.Model(&sharedmodel.WeeklyChampion{}).Preload("Champion").Find(&wcs).Error; err != nil {
			slog.Error(fmt.Sprintf("failed to retrieve weekly champions : %s", err.Error()))
			return err
		}
		for _, wc := range wcs {
			if wc.Champion == nil {
				continue
			}
			include := false
			for _, c := range cs {
				if c.ID == wc.Champion.ID {
					include = true
					break
				}
			}
			if !include {
				cs = append(cs, *wc.Champion)
			}
		}
		return nil
	})
	return cs, err
}

func (g *gameManager) handleCancel(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	db := g.d.Database(r.Context())
	if err := func() error {
		g.gsMu.Lock()
		defer g.gsMu.Unlock()
		if g.gs.GameInProgress && g.gs.RollCount > 0 {
			slog.Info(fmt.Sprintf("[handleCancel] - cancelling the current loi"))
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("game_id = ?", g.gs.GameId).Delete(&sharedmodel.GamePlayerRoll{}).Error; err != nil {
					return err
				}
				if err := tx.Where("game_id = ?", g.gs.GameId).Delete(&sharedmodel.GamePlayer{}).Error; err != nil {
					return err
				}
				if err := tx.Where("id = ?", g.gs.GameId).Delete(&sharedmodel.Game{}).Error; err != nil {
					return err
				}
				return nil
			})
			if err != nil {
				slog.Error("[handleCancel] - " + err.Error())
				return err
			}
			g.gs.GameId = 0
		}
		return nil
	}(); err != nil {
		return
	}
	g.handleReset(wm, conn, r)
}

func (g *gameManager) handleReset(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	db := g.d.Database(r.Context())
	var lVer sharedmodel.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		return
	}
	slog.Info(fmt.Sprintf("[handleReset] - resetting the game state values"))
	g.gs.LeagueVersion = lVer.Version
	g.gs.GameInProgress = false
	g.gs.RollCount = 0
	g.gs.NextRollTimer = 0
	g.gs.CanRoll = true
	slog.Info(fmt.Sprintf("[handleReset] - removing players that are no longer available"))
	for i := range g.gs.Players {
		if _, ok := g.gs.AvailablePlayers[g.gs.Players[i].Player.ID]; !ok {
			g.gs.Players[i] = model.NewEmptyGamePlayer()
		} else {
			g.gs.Players[i].Role = nil
			g.gs.Players[i].Champion = nil
		}
	}

	sgs, err := json.Marshal(*g.gs)
	if err != nil {
		slog.Error(fmt.Sprintf("[handleReset] - failed to marshal game state : %s", err.Error()))
		return
	}
	m := modelwebsocket.Message{
		Action:  modelwebsocket.UpdateState,
		Content: string(sgs),
	}
	g.broadcast(m, nil)
}

func (g *gameManager) handleRefreshDiscord(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
}
