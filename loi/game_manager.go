package loi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"

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
	dm.Session().AddHandler(gm.onDiscordVoiceStateUpdate)
	dm.Session().AddHandler(gm.wtv)
	return gm
}

func (g *gameManager) wtv(s *discordgo.Session, u *discordgo.ThreadMembersUpdate) {
	slog.Info(fmt.Sprintf("%+v", u))
}

func (g *gameManager) onDiscordReady(s *discordgo.Session, e *discordgo.Ready) {
	slog.Info("updating initial list of players from discord")
	ch, err := g.dm.GetConfigChannel()
	if err != nil {
		slog.Error("failed to retrieve channel object when voice state update")
		return
	}
	slog.Info(fmt.Sprintf("%+v", e.Guilds[0].Channels[3]))
	ctx := context.Background()
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	g.gs.DiscordGuild = ch.GuildID
	g.gs.DiscordGuildChannel = ch.Name
	aps := make(map[string]model.AvailablePlayer)
	for _, m := range ch.Members {
		var ap model.AvailablePlayer
		if m.Member == nil {
			slog.Warn("discord api returned empty members for discord channel")
			continue
		}
		if m.Member.User == nil {
			slog.Warn("member is missing user, can't identify user id")
			continue
		} else {
			name := m.Member.Nick
			if name == "" {
				name = m.Member.DisplayName()
			}
			ap = model.AvailablePlayer{
				ID:   &m.Member.User.ID,
				Name: &name,
			}
			if ap.ID == nil {
				slog.Warn("no user id, can't update available player")
				continue
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
	}

	if !g.gs.GameInProgress {
		g.gs.AvailablePlayers = aps
		for i, p := range g.gs.Players {
			if ap, ok := aps[p.Player.ID]; !ok {
				g.gs.Players[i] = model.NewEmptyGamePlayer()
			} else {
				g.gs.Players[i].Player.ID = *ap.ID
				g.gs.Players[i].Player.Name = ap.Name
			}
		}
		for len(g.gs.Players) < 5 {
			g.gs.Players = append(g.gs.Players, model.NewEmptyGamePlayer())
		}
		if len(g.gs.Players) > 5 {
			g.gs.Players = g.gs.Players[:5]
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
	if u.ChannelID != internal.Config().Discord.ChannelID {
		slog.Warn("channel ID is not the supported one : " + u.ChannelID)
		return
	}
	ch, err := g.dm.GetConfigChannel()
	if err != nil {
		slog.Error("failed to retrieve channel object when voice state update")
		return
	}
	ctx := context.Background()
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	g.gs.DiscordGuild = ch.GuildID
	g.gs.DiscordGuildChannel = ch.Name
	aps := make(map[string]model.AvailablePlayer)
	for _, m := range ch.Members {
		var ap model.AvailablePlayer
		if m.Member == nil {
			slog.Warn("discord api returned empty members for discord channel")
			continue
		}
		if m.Member.User == nil {
			slog.Warn("member is missing user, can't identify user id")
			continue
		} else {
			name := m.Member.Nick
			if name == "" {
				name = m.Member.DisplayName()
			}
			ap = model.AvailablePlayer{
				ID:   &m.Member.User.ID,
				Name: &name,
			}
			if ap.ID == nil {
				slog.Warn("no user id, can't update available player")
				continue
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
	}

	if !g.gs.GameInProgress {
		g.gs.AvailablePlayers = aps
		for i, p := range g.gs.Players {
			if ap, ok := aps[p.Player.ID]; !ok {
				g.gs.Players[i] = model.NewEmptyGamePlayer()
			} else {
				g.gs.Players[i].Player.ID = *ap.ID
				g.gs.Players[i].Player.Name = ap.Name
			}
		}
		for len(g.gs.Players) < 5 {
			g.gs.Players = append(g.gs.Players, model.NewEmptyGamePlayer())
		}
		if len(g.gs.Players) > 5 {
			g.gs.Players = g.gs.Players[:5]
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
	conn.WriteJSON(m)
}

func (g *gameManager) broadcast(m interface{}, sender *websocket.Conn) error {
	g.connsMu.RLock()
	defer g.connsMu.RUnlock()
	for _, c := range g.conns {
		if c == sender {
			continue
		}
		c.WriteJSON(m)
	}
	return nil
}

// HandleWebsocket implements GameManager.
func (g *gameManager) HandleWebsocketMessage(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) bool {
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
		sgs, err := json.Marshal(*g.gs)
		if err != nil {
			slog.Error(fmt.Sprintf("failed to marshal game state : %s", err.Error()))
			return
		}
		m := modelwebsocket.Message{
			Action:  modelwebsocket.UpdateState,
			Content: string(sgs),
		}
		conn.WriteJSON(m)
		return
	}

	type content struct {
		ID   string  `json:"id"`
		Name *string `json:"name,omitempty"`
	}
	var cs []content
	if err := json.Unmarshal([]byte(wm.Content), &cs); err != nil {
		slog.Error(err.Error())
		return
	}

	gps := make([]model.GamePlayer, len(cs))
	for i, c := range cs {
		gps[i] = model.GamePlayer{
			Player: model.DiscordPlayer{
				ID:   c.ID,
				Name: c.Name,
			},
		}
	}
	for i := range g.gs.Players {
		// TODO: Validate with discord players
		if i < len(gps) {
			g.gs.Players[i] = gps[i]
		} else {
			g.gs.Players[i] = model.NewEmptyGamePlayer()
		}
	}
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

func (g *gameManager) handleRoll(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	ctx := r.Context()
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	if !g.gs.CanRoll {
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

	gprs := make([]sharedmodel.GamePlayerRoll, 0)
	rcs := make([]sharedmodel.Champion, 0, 5)
	roles := model.NewRoleSlice().Shuffle()
	for i, p := range g.gs.Players {
		g.gs.Players[i].Role = &roles[i]
		if p.Player.ID == "" {
			continue
		}
		var rc sharedmodel.Champion
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

	if err := db.Model(&sharedmodel.GamePlayerRoll{}).Create(&gprs).Error; err != nil {
		slog.Error("failed to update database with game player roll")
		return
	}

	// TODO: Start a goroutine that execute each second to uppdate connection states
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
	var lVer sharedmodel.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		slog.Error(err.Error())
		return
	}
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	g.gs.LeagueVersion = lVer.Version
	if g.gs.GameInProgress && g.gs.RollCount > 0 {
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
			slog.Error(err.Error())
			return
		}
		g.gs.GameId -= 1
	}
	g.gs.GameInProgress = false
	g.gs.RollCount = 0
	for i := range g.gs.Players {
		g.gs.Players[i].Role = nil
		g.gs.Players[i].Champion = nil
	}
	// TODO: Reset countdown goroutine
	g.gs.NextRollTimer = 0
	g.gs.CanRoll = true
	// TODO: Update player list from discord

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

func (g *gameManager) handleReset(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	g.gsMu.Lock()
	defer g.gsMu.Unlock()
	db := g.d.Database(r.Context())
	var lVer sharedmodel.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		return
	}
	g.gs.LeagueVersion = lVer.Version
	g.gs.GameInProgress = false
	g.gs.RollCount = 0
	for i := range g.gs.Players {
		g.gs.Players[i].Role = nil
		g.gs.Players[i].Champion = nil
	}
	// TODO: Stop a goroutine that execute each second to uppdate connection states
	g.gs.NextRollTimer = 0
	g.gs.CanRoll = true
	// TODO: Update the player list from discord
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

func (g *gameManager) handleRefreshDiscord(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
}
