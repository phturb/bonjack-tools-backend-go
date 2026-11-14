// Package loi provides the core game management engine for "Loi des Norms" games.
// It handles game state management, player assignments, champion rolling, and real-time
// WebSocket communication with web clients.
//
// Key components:
//   - GameManager: Central coordinator for game state and Discord integration
//   - GameController: Implements game flow logic and champion assignment
//   - WebSocket handlers for real-time updates and client communication
//   - Statistics tracking and player performance analytics
//
// The package integrates with Discord for voice channel monitoring and player detection,
// manages in-memory game state, and persists game data through the database models.
// It serves as the business logic hub connecting Discord events, player actions,
// and game statistics.
package loi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/phturb/bonjack-tools-backend-go/internal/logger"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/discord"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GameManager interface {
	HandleWebsocketMessage(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) bool
	HandleWebsocketConnection(conn *websocket.Conn, r *http.Request)
	GameController
}

type gameManager struct {
	d internal.Dependencies

	dm discord.DiscordManager

	// mu guards the voice pool and the state of the lobby (not its conns).
	mu   sync.RWMutex
	pool voicePool
	main *lobby

	// connsMu guards the websocket conns of the lobby.
	connsMu sync.RWMutex

	*gameController

	dCleanup func()
}

var _ GameManager = (*gameManager)(nil)

func NewGameManager(d internal.Dependencies, dm discord.DiscordManager) (GameManager, error) {
	gm := &gameManager{
		d:    d,
		dm:   dm,
		pool: voicePool{players: make(map[string]model.AvailablePlayer)},
		main: newLobby(mainLobbyID, mainLobbyName),
		gameController: &gameController{
			d: d,
		},
	}
	gm.dCleanup = gm.registryDiscordEventHandlers(dm.Session())

	if err := gm.bootstrap(context.Background()); err != nil {
		logger.ComponentLogger("NewGameManager").Error("failed to restore in-progress game", "error", err)
	}

	ticker := time.NewTicker(rollTimerTick)
	go func() {
		for range ticker.C {
			gm.tickRollTimer(uint(rollTimerTick / time.Millisecond))
		}
	}()
	return gm, nil
}

// rollTimerTick is how often the roll cooldown is decremented.
const rollTimerTick = 1 * time.Second

// tickRollTimer decrements the roll cooldown by elapsedMs (clamped at zero,
// NextRollTimer is unsigned) and re-enables rolling once it reaches zero.
func (g *gameManager) tickRollTimer(elapsedMs uint) {
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	l := g.main
	if l.gs.NextRollTimer > elapsedMs {
		l.gs.NextRollTimer -= elapsedMs
		return
	}
	l.gs.NextRollTimer = 0
	if !l.gs.CanRoll {
		l.gs.CanRoll = true
		g.broadcastState(l, nil)
	}
}

func updateDiscordMessage(messageID string, s *discordgo.Session, gs *model.GameState) error {
	em, c, err := createDiscordMessage(gs)
	if err != nil {
		return err
	}
	_, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel: config.Config().Discord.ChannelID,
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

// restoreMaxAge bounds how old the last roll of an unfinished game may be for
// bootstrap to restore it. A loi is played in one sitting; anything older is a
// game that was never closed and must not come back on every deploy.
const restoreMaxAge = 6 * time.Hour

// closeStaleGames marks every unfinished game other than keepID as finished
// at its last update, so a game that is abandoned (or predates finished_at
// tracking) is never restored again by a later restart.
func closeStaleGames(db *gorm.DB, keepID uint) (int64, error) {
	res := db.Model(&modeldb.Game{}).
		Where("finished_at IS NULL AND id <> ?", keepID).
		Update("finished_at", gorm.Expr("updated_at"))
	return res.RowsAffected, res.Error
}

// bootstrap restores the last unfinished game (if any) after a restart so a
// loi in progress survives a deploy. Only a game whose last roll is younger
// than restoreMaxAge is restored; every other unfinished game is closed.
func (g *gameManager) bootstrap(ctx context.Context) error {
	l := logger.ComponentLogger("bootstrap")
	db := g.d.Database(ctx)
	var lastGame modeldb.Game
	if err := db.Model(&modeldb.Game{}).Preload("Rolls").Last(&lastGame).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	rollCount := uint(0)
	var lastRollAt time.Time
	for _, r := range lastGame.Rolls {
		if r.RollNumber > rollCount {
			rollCount = r.RollNumber
		}
		if r.CreatedAt.After(lastRollAt) {
			lastRollAt = r.CreatedAt
		}
	}
	restorable := lastGame.FinishedAt == nil && rollCount > 0 && time.Since(lastRollAt) <= restoreMaxAge
	keepID := uint(0)
	if restorable {
		keepID = lastGame.ID
	}
	if n, err := closeStaleGames(db, keepID); err != nil {
		l.Error("failed to close stale games", "error", err)
	} else if n > 0 {
		l.Info("closed stale unfinished games", "count", n)
	}
	if !restorable {
		return nil
	}
	var lVer modeldb.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		return fmt.Errorf("failed to retrieve version: %w", err)
	}
	var lastRolls []modeldb.GamePlayerRoll
	if err := db.Preload("Player").Preload("Champion").
		Where("game_id = ? AND roll_number = ?", lastGame.ID, rollCount).
		Find(&lastRolls).Error; err != nil {
		return err
	}

	unlock := g.lockAndUpdateGameState()
	defer unlock()
	gs := g.main.gs
	gs.LeagueVersion = lVer.Version
	gs.GameID = lastGame.ID
	gs.GameInProgress = true
	gs.RollCount = rollCount
	gs.NextRollTimer = 0
	gs.CanRoll = true
	players := make([]model.GamePlayer, 0, 5)
	for _, r := range lastRolls {
		role := model.Role(r.Role)
		champion := r.Champion
		players = append(players, model.GamePlayer{
			Player:   model.DiscordPlayer{ID: r.PlayerID, Name: r.Player.Name},
			Role:     &role,
			Champion: model.ChampionFromDB(&champion),
		})
	}
	for len(players) < 5 {
		players = append(players, model.NewEmptyGamePlayer())
	}
	gs.Players = players
	l.Info("restored in-progress game", "game_id", lastGame.ID, "roll_count", rollCount)
	return nil
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
		if err := g.d.Database(ctx).Model(&modeldb.Player{}).Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(&modeldb.Player{
			ID:   *ap.ID,
			Name: ap.Name,
		}).Error; err != nil {
			l.Warn("[playersFromMembers] - failed to update player in database : " + err.Error())
			continue
		}
	}

	g.pool.players = aps
	gs := g.main.gs
	if !gs.GameInProgress {
		for i, p := range gs.Players {
			if p.Player.ID == "" {
				continue
			}
			if ap, ok := aps[p.Player.ID]; !ok {
				l.Warn(fmt.Sprintf("[playersFromMembers] - player not found '%s' in game state", p.Player.ID))
				gs.Players[i] = model.NewEmptyGamePlayer()
			} else {
				gs.Players[i].Player.ID = *ap.ID
				gs.Players[i].Player.Name = ap.Name
			}
		}
		for len(gs.Players) < 5 {
			gs.Players = append(gs.Players, model.NewEmptyGamePlayer())
		}
	}
	g.broadcastState(g.main, nil)
}

// HandleWebsocketConnection implements GameManager.
func (g *gameManager) HandleWebsocketConnection(conn *websocket.Conn, r *http.Request) {
	l := logger.ComponentLogger("HandleWebsocketConnection")
	if r == nil {
		l.Info("handling websocket disconnection")
		g.connsMu.Lock()
		defer g.connsMu.Unlock()
		lb := g.main
		newConns := make([]*websocket.Conn, 0, len(lb.conns))
		for _, c := range lb.conns {
			if c == conn {
				continue
			}
			newConns = append(newConns, c)
		}
		lb.conns = newConns
		return
	}
	l.Info("handling websocket connection")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	lb := g.main
	sgs, err := json.Marshal(g.stateOf(lb, false))
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
	lb.conns = append(lb.conns, conn)
	l.Info("[HandleWebsocketConnection] - sending game state to the new connection")
	if err := conn.WriteJSON(m); err != nil {
		l.Error(err.Error())
	}
}

func (g *gameManager) broadcast(lb *lobby, m interface{}, sender *websocket.Conn) error {
	l := logger.ComponentLogger("broadcast")
	l.Info("broadcasting message")
	g.connsMu.RLock()
	defer g.connsMu.RUnlock()
	var errs []error
	for _, c := range lb.conns {
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

func (g *gameManager) broadcastState(lb *lobby, sender *websocket.Conn) {
	l := logger.ComponentLogger("broadcastState")
	gs := g.stateOf(lb, true)
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
		if err := g.broadcast(lb, m, nil); err != nil {
			l.Error(fmt.Sprintf("failed to broadcast game state : %s", err.Error()))
		}
	}
	if lb.messageID != nil {
		if err := updateDiscordMessage(*lb.messageID, g.dm.Session(), &gs); err != nil {
			l.Error(fmt.Sprintf("failed to update discord message on broadcast: %s", err.Error()))
		}
	}
}

func (g *gameManager) handleUpdatePlayers(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	l := logger.ComponentLogger("handleUpdatePlayers")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	lb := g.main
	if lb.gs.GameInProgress {
		l.Warn("[handleUpdatePlayers] - game is in progress, skipping update")
		g.broadcastState(lb, conn)
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
		if ap, ok := g.pool.players[c.ID]; ok {
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
	lb.gs.Players = gps
	g.broadcastState(lb, nil)
}

func (g *gameManager) handleRoll(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	g.roll(r.Context(), g.main)
}

// pickChampion returns a random champion from pool that is not already taken.
// When every champion in the pool is taken it falls back to any champion of the
// pool rather than failing the roll.
func pickChampion(pool []modeldb.Champion, taken map[string]struct{}) modeldb.Champion {
	free := make([]modeldb.Champion, 0, len(pool))
	for _, c := range pool {
		if _, ok := taken[c.ID]; !ok {
			free = append(free, c)
		}
	}
	if len(free) == 0 {
		free = pool
	}
	return free[rand.Intn(len(free))]
}

func (g *gameManager) roll(ctx context.Context, lb *lobby) {
	l := logger.ComponentLogger("roll")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	if !lb.gs.CanRoll {
		l.Info("game is not allowed to roll")
		return
	}
	db := g.d.Database(ctx)
	var lVer modeldb.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		l.Error("failed to retrieve version", "error", err)
		return
	}

	// 1. Decide roles and champions without touching the game state.
	rollNumber := lb.gs.RollCount + 1
	roles := model.NewRoleSlice().Shuffle()
	type assignment struct {
		role     model.Role
		champion *modeldb.Champion
	}
	assignments := make([]assignment, len(lb.gs.Players))
	taken := map[string]struct{}{}
	for i, p := range lb.gs.Players {
		if i >= len(roles) {
			break
		}
		assignments[i].role = roles[i]
		if p.Player.ID == "" {
			continue
		}
		pool, err := g.retrieveChampionsForPlayer(ctx, p)
		if err != nil {
			l.Error("failed to retrieve player champions, aborting roll", "player_id", p.Player.ID, "error", err)
			return
		}
		if len(pool) == 0 {
			l.Error("no champion to select from, aborting roll", "player_id", p.Player.ID)
			return
		}
		rc := pickChampion(pool, taken)
		taken[rc.ID] = struct{}{}
		assignments[i].champion = &rc
		l.Info("assigned champion", "player_id", p.Player.ID, "role", roles[i], "champion", rc.Name)
	}

	// 2. Persist game, players and rolls in one transaction.
	gameID := lb.gs.GameID
	newGame := !lb.gs.GameInProgress
	err := db.Transaction(func(tx *gorm.DB) error {
		if newGame {
			game := modeldb.Game{}
			if err := tx.Model(&modeldb.Game{}).Create(&game).Error; err != nil {
				return err
			}
			gps := make([]modeldb.GamePlayer, 0, len(lb.gs.Players))
			for _, p := range lb.gs.Players {
				if p.Player.ID == "" {
					continue
				}
				gps = append(gps, modeldb.GamePlayer{GameID: game.ID, PlayerID: p.Player.ID})
			}
			if len(gps) > 0 {
				if err := tx.Model(&modeldb.GamePlayer{}).Create(&gps).Error; err != nil {
					return err
				}
			}
			gameID = game.ID
		}
		gprs := make([]modeldb.GamePlayerRoll, 0, len(lb.gs.Players))
		for i, p := range lb.gs.Players {
			if assignments[i].champion == nil {
				continue
			}
			gprs = append(gprs, modeldb.GamePlayerRoll{
				GameID:     gameID,
				PlayerID:   p.Player.ID,
				RollNumber: rollNumber,
				Role:       string(assignments[i].role),
				ChampionID: assignments[i].champion.ID,
			})
		}
		if len(gprs) > 0 {
			if err := tx.Model(&modeldb.GamePlayerRoll{}).Create(&gprs).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		l.Error("failed to persist roll, game state unchanged", "error", err)
		return
	}

	// 3. Commit to the in-memory state only once the database agreed.
	if newGame {
		l.Info("loi des norms has started", "game_id", gameID)
	}
	lb.gs.GameID = gameID
	lb.gs.GameInProgress = true
	lb.gs.LeagueVersion = lVer.Version
	lb.gs.RollCount = rollNumber
	lb.gs.CanRoll = false
	lb.gs.NextRollTimer = config.Config().GameManager.TimerTime
	for i := range lb.gs.Players {
		if i >= len(assignments) {
			break
		}
		role := assignments[i].role
		lb.gs.Players[i].Role = &role
		lb.gs.Players[i].Champion = model.ChampionFromDB(assignments[i].champion)
	}
	l.Info("roll completed", "game_id", gameID, "roll_number", rollNumber)
	g.broadcastState(lb, nil)
}

func (g *gameManager) retrieveChampionsForPlayer(ctx context.Context, gp model.GamePlayer) ([]modeldb.Champion, error) {
	l := logger.ComponentLogger("retrieveChampionsForPlayer")
	cs := make([]modeldb.Champion, 0)
	err := g.d.Database(ctx).Transaction(func(tx *gorm.DB) error {
		var p modeldb.Player
		if err := tx.Model(&modeldb.Player{}).Preload("Champions").Where("id = ?", gp.Player.ID).Find(&p).Error; err != nil {
			l.Error(fmt.Sprintf("failed to retrieve player champions : %s", err.Error()))
			return err
		}
		cs = append(cs, p.Champions...)
		if len(cs) <= 0 {
			if err := tx.Model(&modeldb.Champion{}).Find(&cs).Error; err != nil {
				l.Error(fmt.Sprintf("failed to retrieve all champions : %s", err.Error()))
				return err
			}
		}
		wcs := make([]modeldb.WeeklyChampion, 0)
		if err := tx.Model(&modeldb.WeeklyChampion{}).Preload("Champion").Find(&wcs).Error; err != nil {
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
	g.cancel(r.Context(), g.main)
}

func (g *gameManager) cancel(ctx context.Context, lb *lobby) {
	l := logger.ComponentLogger("cancel")
	db := g.d.Database(ctx)
	if err := func() error {
		unlock := g.lockAndUpdateGameState()
		defer unlock()
		if lb.gs.GameInProgress && lb.gs.RollCount > 0 {
			l.Info(fmt.Sprintf("cancelling the current loi"))
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("game_id = ?", lb.gs.GameID).Delete(&modeldb.GamePlayerRoll{}).Error; err != nil {
					return err
				}
				if err := tx.Where("game_id = ?", lb.gs.GameID).Delete(&modeldb.GamePlayer{}).Error; err != nil {
					return err
				}
				if err := tx.Where("id = ?", lb.gs.GameID).Delete(&modeldb.Game{}).Error; err != nil {
					return err
				}
				return nil
			})
			if err != nil {
				l.Error("[handleCancel] - " + err.Error())
				return err
			}
			lb.gs.GameID = 0
		}
		return nil
	}(); err != nil {
		return
	}
	g.reset(ctx, lb)
}

func (g *gameManager) handleReset(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
	g.reset(r.Context(), g.main)
}

func (g *gameManager) reset(ctx context.Context, lb *lobby) {
	l := logger.ComponentLogger("reset")
	unlock := g.lockAndUpdateGameState()
	defer unlock()
	db := g.d.Database(ctx)
	var lVer modeldb.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		return
	}
	l.Info("resetting the game state values")
	if lb.gs.RollCount != 0 {
		if err := db.Model(&modeldb.Game{}).Where("id = ?", lb.gs.GameID).Update("finished_at", time.Now()).Error; err != nil {
			l.WithError(err).Error("failed to finishedAt game field")
		}
	}
	content := fmt.Sprintf("Loi is finished or canceled after %d roll", lb.gs.RollCount)
	lb.gs.LeagueVersion = lVer.Version
	lb.gs.GameInProgress = false
	lb.gs.RollCount = 0
	lb.gs.NextRollTimer = 0
	lb.gs.CanRoll = true
	if lb.messageID != nil {
		_, err := g.dm.Session().ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel: config.Config().Discord.ChannelID,
			ID:      *lb.messageID,
			Content: &content,
		})
		if err != nil {
			l.WithError(err).Error("failed to update channel message")
		}
	}
	lb.messageID = nil
	l.Info("removing players that are no longer available")
	for i := range lb.gs.Players {
		if _, ok := g.pool.players[lb.gs.Players[i].Player.ID]; !ok {
			lb.gs.Players[i] = model.NewEmptyGamePlayer()
		} else {
			lb.gs.Players[i].Role = nil
			lb.gs.Players[i].Champion = nil
		}
	}
	g.broadcastState(lb, nil)
}

func (g *gameManager) handleRefreshDiscord(wm *modelwebsocket.Message, conn *websocket.Conn, r *http.Request) {
}

func (g *gameManager) lockAndUpdateGameState() func() {
	g.mu.Lock()
	return g.mu.Unlock
}
