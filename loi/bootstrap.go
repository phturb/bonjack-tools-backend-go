package loi

import (
	"context"
	"fmt"
	"time"

	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"gorm.io/gorm"
)

// restoreMaxAge bounds how old the last roll of an unfinished game may be for
// bootstrap to restore it. A loi is played in one sitting; anything older is a
// game that was never closed and must not come back on every deploy.
const restoreMaxAge = 6 * time.Hour

// closeStaleGames marks every unfinished game other than keepIDs as finished
// at its last update, so a game that is abandoned (or predates finished_at
// tracking) is never restored again by a later restart.
func closeStaleGames(db *gorm.DB, keepIDs []uint) (int64, error) {
	q := db.Model(&modeldb.Game{}).Where("finished_at IS NULL")
	if len(keepIDs) > 0 {
		q = q.Where("id NOT IN ?", keepIDs)
	}
	res := q.Update("finished_at", gorm.Expr("updated_at"))
	return res.RowsAffected, res.Error
}

// restorable is an unfinished game bootstrap brings back.
type restorable struct {
	game      modeldb.Game
	rollCount uint
	rolls     []modeldb.GamePlayerRoll // the latest roll, one row per player
}

// bootstrap restores the unfinished games after a restart so a loi in
// progress survives a deploy. Every unfinished game whose last roll is
// younger than restoreMaxAge comes back: the newest into main, each other
// one into its own lobby named "Restored game #<id>". A game that shares a
// player with a newer restored game, or that does not fit in the lobby
// limit, is closed with every other unfinished game.
func (g *gameManager) bootstrap(ctx context.Context) error {
	l := logger.ComponentLogger("bootstrap")
	db := g.d.Database(ctx)
	var open []modeldb.Game
	if err := db.Model(&modeldb.Game{}).Preload("Rolls").
		Where("finished_at IS NULL").Order("id DESC").Find(&open).Error; err != nil {
		return err
	}

	var keep []restorable
	taken := map[string]uint{} // player id -> restored game id
	for _, game := range open {
		rollCount := uint(0)
		var lastRollAt time.Time
		for _, r := range game.Rolls {
			if r.RollNumber > rollCount {
				rollCount = r.RollNumber
			}
			if r.CreatedAt.After(lastRollAt) {
				lastRollAt = r.CreatedAt
			}
		}
		if rollCount == 0 || time.Since(lastRollAt) > restoreMaxAge {
			continue
		}
		if len(keep) >= g.cfg.maxLobbies {
			l.Warn("no lobby left for an unfinished game, closing it", "game_id", game.ID)
			continue
		}
		var latest []modeldb.GamePlayerRoll
		for _, r := range game.Rolls {
			if r.RollNumber == rollCount {
				latest = append(latest, r)
			}
		}
		conflict := false
		for _, r := range latest {
			if newer, ok := taken[r.PlayerID]; ok {
				l.Warn("unfinished game shares a player with a newer one, closing it",
					"game_id", game.ID, "newer_game_id", newer, "player_id", r.PlayerID)
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		for _, r := range latest {
			taken[r.PlayerID] = game.ID
		}
		keep = append(keep, restorable{game: game, rollCount: rollCount})
	}

	keepIDs := make([]uint, 0, len(keep))
	for _, k := range keep {
		keepIDs = append(keepIDs, k.game.ID)
	}
	if n, err := closeStaleGames(db, keepIDs); err != nil {
		l.Error("failed to close stale games", "error", err)
	} else if n > 0 {
		l.Info("closed stale unfinished games", "count", n)
	}
	if len(keep) == 0 {
		return nil
	}

	var lVer modeldb.LeagueVersion
	if err := db.First(&lVer).Error; err != nil {
		return fmt.Errorf("failed to retrieve version: %w", err)
	}
	for i := range keep {
		if err := db.Preload("Player").Preload("Champion").
			Where("game_id = ? AND roll_number = ?", keep[i].game.ID, keep[i].rollCount).
			Find(&keep[i].rolls).Error; err != nil {
			return err
		}
	}

	unlock := g.lockAndUpdateGameState()
	defer unlock()
	for i, k := range keep {
		lb := g.main
		if i > 0 {
			lb = g.addLobby(fmt.Sprintf("Restored game #%d", k.game.ID))
		}
		gs := lb.gs
		gs.LeagueVersion = lVer.Version
		gs.GameID = k.game.ID
		gs.GameInProgress = true
		gs.RollCount = k.rollCount
		gs.NextRollTimer = 0
		gs.CanRoll = true
		players := make([]model.GamePlayer, 0, 5)
		for _, r := range k.rolls {
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
		l.Info("restored in-progress game", "game_id", k.game.ID, "roll_count", k.rollCount, "lobby_id", lb.id)
	}
	return nil
}
