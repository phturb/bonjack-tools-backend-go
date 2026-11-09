package swiss

import (
	"context"
	"errors"
	"fmt"
	mathrand "math/rand"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
	"gorm.io/gorm"
)

// Error is a service error carrying the HTTP status the handler must answer.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func errNotFound() *Error { return &Error{http.StatusNotFound, "tournament not found"} }
func errForbidden() *Error {
	return &Error{http.StatusForbidden, "only the creator can modify this tournament"}
}
func errConflict(msg string) *Error   { return &Error{http.StatusConflict, msg} }
func errValidation(msg string) *Error { return &Error{http.StatusUnprocessableEntity, msg} }

// Service holds every database operation of the package.
type Service struct {
	deps internal.Dependencies
	log  *logger.Logger
}

// NewService creates the service.
func NewService(deps internal.Dependencies) *Service {
	return &Service{deps: deps, log: logger.ComponentLogger("swiss")}
}

// ---- Reads -----------------------------------------------------------------

// List returns the public tournaments, newest first.
func (s *Service) List(ctx context.Context) ([]TournamentSummary, error) {
	db := s.deps.Database(ctx)
	var ts []Tournament
	if err := db.Where("visibility = ?", VisibilityPublic).Order("created_at DESC").Find(&ts).Error; err != nil {
		return nil, err
	}
	out := make([]TournamentSummary, 0, len(ts))
	for _, t := range ts {
		var count int64
		if err := db.Model(&Participant{}).Where("tournament_id = ?", t.ID).Count(&count).Error; err != nil {
			return nil, err
		}
		out = append(out, TournamentSummary{
			ID: t.ID, Name: t.Name, Status: t.Status,
			Participants: int(count), CreatedAt: t.CreatedAt,
		})
	}
	return out, nil
}

// Get returns the full tournament.
func (s *Service) Get(ctx context.Context, id string) (*TournamentDTO, error) {
	return s.load(s.deps.Database(ctx), id)
}

// loaded is a tournament with its rows, read inside one transaction.
type loaded struct {
	t            Tournament
	participants []Participant // by seed
	matches      []Match       // by round, created_at, id
}

func (l *loaded) currentRound() int {
	n := 0
	for _, m := range l.matches {
		if m.Round > n {
			n = m.Round
		}
	}
	return n
}

func (l *loaded) pairingConfig() PairingConfig {
	return PairingConfig{
		WinsToAdvance:     l.t.WinsToAdvance,
		LossesToEliminate: l.t.LossesToEliminate,
		BestOf:            l.t.BestOf,
		DecidersBestOf:    l.t.DecidersBestOf,
		AllowRematches:    l.t.AllowRematches,
	}
}

func fetch(db *gorm.DB, id string) (*loaded, error) {
	var l loaded
	if err := db.First(&l.t, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errNotFound()
		}
		return nil, err
	}
	if err := db.Where("tournament_id = ?", id).Order("seed ASC, id ASC").Find(&l.participants).Error; err != nil {
		return nil, err
	}
	if err := db.Where("tournament_id = ?", id).Order("round ASC, created_at ASC, id ASC").Find(&l.matches).Error; err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *Service) load(db *gorm.DB, id string) (*TournamentDTO, error) {
	l, err := fetch(db, id)
	if err != nil {
		return nil, err
	}
	return toDTO(l), nil
}

func toDTO(l *loaded) *TournamentDTO {
	cfg := l.pairingConfig()
	standings, h2h := ComputeStandings(l.participants, l.matches)
	cur := l.currentRound()

	dto := &TournamentDTO{
		ID: l.t.ID, Name: l.t.Name, Description: l.t.Description, Status: l.t.Status,
		Config: ConfigDTO{
			WinsToAdvance: l.t.WinsToAdvance, LossesToEliminate: l.t.LossesToEliminate,
			BestOf: l.t.BestOf, DecidersBestOf: l.t.DecidersBestOf, Seeding: l.t.Seeding,
			AllowRematches: l.t.AllowRematches, Visibility: l.t.Visibility,
		},
		OwnerSub: l.t.OwnerSub, CreatedAt: l.t.CreatedAt, CurrentRound: cur,
		Participants: make([]ParticipantDTO, 0, len(l.participants)),
		Rounds:       make([]RoundDTO, 0, cur),
		Standings:    SortStandings(standings, h2h),
	}
	for i, p := range l.participants {
		st := standings[i]
		dto.Participants = append(dto.Participants, ParticipantDTO{
			ID: p.ID, Name: p.Name, Seed: p.Seed,
			Wins: st.Wins, Losses: st.Losses, Buchholz: st.Buchholz, Status: st.Status(cfg),
		})
	}
	for r := 1; r <= cur; r++ {
		dto.Rounds = append(dto.Rounds, RoundDTO{
			Number:  r,
			Closed:  r < cur || l.t.Status == StatusCompleted,
			Matches: []MatchDTO{},
		})
	}
	for _, m := range l.matches {
		rd := &dto.Rounds[m.Round-1]
		rd.Matches = append(rd.Matches, MatchDTO{
			ID: m.ID, Bucket: BucketDTO{m.BucketWins, m.BucketLosses}, BestOf: m.BestOf,
			P1: m.P1ID, P2: m.P2ID, P1Score: m.P1Score, P2Score: m.P2Score, Winner: m.WinnerID,
			Bye: m.Bye, Rematch: m.Rematch, Forfeit: m.Forfeit,
		})
	}
	return dto
}

// ---- Mutations -------------------------------------------------------------

// withOwned runs fn inside a transaction after loading the tournament and
// checking ownership.
func (s *Service) withOwned(ctx context.Context, subject, id string, fn func(tx *gorm.DB, l *loaded) error) error {
	return s.deps.Database(ctx).Transaction(func(tx *gorm.DB) error {
		l, err := fetch(tx, id)
		if err != nil {
			return err
		}
		if l.t.OwnerSub != subject {
			s.log.Warn("tournament mutation refused", "tournament_id", id, "subject", subject)
			return errForbidden()
		}
		return fn(tx, l)
	})
}

// mutate is withOwned followed by a reload of the fresh DTO.
func (s *Service) mutate(ctx context.Context, subject, id string, fn func(tx *gorm.DB, l *loaded) error) (*TournamentDTO, error) {
	var dto *TournamentDTO
	err := s.withOwned(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if err := fn(tx, l); err != nil {
			return err
		}
		var err error
		dto, err = s.load(tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return dto, nil
}

func validBestOf(n int) bool     { return n == 1 || n == 3 || n == 5 }
func validRange(n int) bool      { return n >= 1 && n <= 5 }
func validSeeding(v string) bool { return v == SeedingManual || v == SeedingRandom }
func validVisibility(v string) bool {
	return v == VisibilityPublic || v == VisibilityUnlisted
}

// applyConfig copies the provided fields onto t and validates the result.
func applyConfig(t *Tournament, in ConfigInput) error {
	if in.Name != nil {
		t.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		t.Description = strings.TrimSpace(*in.Description)
	}
	if in.WinsToAdvance != nil {
		t.WinsToAdvance = *in.WinsToAdvance
	}
	if in.LossesToEliminate != nil {
		t.LossesToEliminate = *in.LossesToEliminate
	}
	if in.BestOf != nil {
		t.BestOf = *in.BestOf
	}
	if in.DecidersBestOf != nil {
		t.DecidersBestOf = *in.DecidersBestOf
	}
	if in.Seeding != nil {
		t.Seeding = *in.Seeding
	}
	if in.AllowRematches != nil {
		t.AllowRematches = *in.AllowRematches
	}
	if in.Visibility != nil {
		t.Visibility = *in.Visibility
	}
	switch {
	case len(t.Name) < 1 || len(t.Name) > 80:
		return errValidation("name must be 1 to 80 characters")
	case len(t.Description) > 500:
		return errValidation("description must be at most 500 characters")
	case !validRange(t.WinsToAdvance):
		return errValidation("winsToAdvance must be between 1 and 5")
	case !validRange(t.LossesToEliminate):
		return errValidation("lossesToEliminate must be between 1 and 5")
	case !validBestOf(t.BestOf):
		return errValidation("bestOf must be 1, 3 or 5")
	case !validBestOf(t.DecidersBestOf):
		return errValidation("decidersBestOf must be 1, 3 or 5")
	case !validSeeding(t.Seeding):
		return errValidation("seeding must be manual or random")
	case !validVisibility(t.Visibility):
		return errValidation("visibility must be public or unlisted")
	}
	return nil
}

// Create creates a draft tournament owned by subject.
func (s *Service) Create(ctx context.Context, subject string, in ConfigInput) (*TournamentDTO, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	seed, err := NewSeed()
	if err != nil {
		return nil, err
	}
	t := Tournament{
		ID: id, Status: StatusDraft, OwnerSub: subject, RNGSeed: seed,
		WinsToAdvance: 3, LossesToEliminate: 3, BestOf: 1,
		Seeding: SeedingManual, Visibility: VisibilityPublic,
	}
	if in.DecidersBestOf == nil {
		in.DecidersBestOf = in.BestOf
		if in.DecidersBestOf == nil {
			in.DecidersBestOf = &t.BestOf
		}
	}
	if err := applyConfig(&t, in); err != nil {
		return nil, err
	}
	var dto *TournamentDTO
	err = s.deps.Database(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&t).Error; err != nil {
			return err
		}
		var err error
		dto, err = s.load(tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("tournament created", "tournament_id", id, "subject", subject)
	return dto, nil
}

// Update patches the configuration. After start only name and description may change.
func (s *Service) Update(ctx context.Context, subject, id string, in ConfigInput) (*TournamentDTO, error) {
	return s.mutate(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if l.t.Status != StatusDraft {
			frozen := in.WinsToAdvance != nil || in.LossesToEliminate != nil || in.BestOf != nil ||
				in.DecidersBestOf != nil || in.Seeding != nil || in.AllowRematches != nil || in.Visibility != nil
			if frozen {
				return errConflict("configuration is frozen after start; only name and description can change")
			}
		}
		if err := applyConfig(&l.t, in); err != nil {
			return err
		}
		return tx.Save(&l.t).Error
	})
}

// Delete removes the tournament and its rows.
func (s *Service) Delete(ctx context.Context, subject, id string) error {
	return s.withOwned(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if err := tx.Where("tournament_id = ?", id).Delete(&Match{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tournament_id = ?", id).Delete(&Participant{}).Error; err != nil {
			return err
		}
		s.log.Info("tournament deleted", "tournament_id", id, "subject", subject)
		return tx.Delete(&Tournament{}, "id = ?", id).Error
	})
}

// ReplaceParticipants replaces the whole participant list. Draft only.
func (s *Service) ReplaceParticipants(ctx context.Context, subject, id string, in []ParticipantInput) (*TournamentDTO, error) {
	return s.mutate(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if l.t.Status != StatusDraft {
			return errConflict("participants can only be edited before start")
		}
		existing := map[string]bool{}
		for _, p := range l.participants {
			existing[p.ID] = true
		}
		seen := map[string]bool{}
		rows := make([]Participant, 0, len(in))
		for i, e := range in {
			name := strings.TrimSpace(e.Name)
			if len(name) < 1 || len(name) > 80 {
				return errValidation("participant names must be 1 to 80 characters")
			}
			key := strings.ToLower(name)
			if seen[key] {
				return errValidation(fmt.Sprintf("duplicate participant name: %s", name))
			}
			seen[key] = true
			pid := e.ID
			if pid == "" || !existing[pid] {
				var err error
				if pid, err = NewID(); err != nil {
					return err
				}
			}
			seed := e.Seed
			if seed <= 0 {
				seed = i + 1
			}
			rows = append(rows, Participant{ID: pid, TournamentID: id, Name: name, Seed: seed})
		}
		// Normalise seeds to 1..n in the requested order.
		sort.SliceStable(rows, func(a, b int) bool { return rows[a].Seed < rows[b].Seed })
		for i := range rows {
			rows[i].Seed = i + 1
		}
		if err := tx.Where("tournament_id = ?", id).Delete(&Participant{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	})
}

// Start applies the seeding and generates round 1.
func (s *Service) Start(ctx context.Context, subject, id string) (*TournamentDTO, error) {
	return s.mutate(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if l.t.Status != StatusDraft {
			return errConflict("tournament already started")
		}
		if len(l.participants) < 2 {
			return errValidation("at least two participants are required")
		}
		if l.t.Seeding == SeedingRandom {
			rng := mathrand.New(mathrand.NewSource(l.t.RNGSeed))
			rng.Shuffle(len(l.participants), func(i, j int) {
				l.participants[i], l.participants[j] = l.participants[j], l.participants[i]
			})
			for i := range l.participants {
				l.participants[i].Seed = i + 1
				if err := tx.Model(&Participant{}).Where("id = ?", l.participants[i].ID).Update("seed", i+1).Error; err != nil {
					return err
				}
			}
		}
		standings, _ := ComputeStandings(l.participants, nil)
		if err := s.insertRound(tx, l, 1, PairRound(l.pairingConfig(), standings)); err != nil {
			return err
		}
		l.t.Status = StatusInProgress
		s.log.Info("tournament started", "tournament_id", id, "participants", len(l.participants))
		return tx.Save(&l.t).Error
	})
}

func (s *Service) insertRound(tx *gorm.DB, l *loaded, round int, planned []PlannedMatch) error {
	now := time.Now()
	rows := make([]Match, 0, len(planned))
	for i, pm := range planned {
		mid, err := NewID()
		if err != nil {
			return err
		}
		m := Match{
			ID: mid, TournamentID: l.t.ID, Round: round,
			BucketWins: pm.BucketWins, BucketLosses: pm.BucketLosses,
			P1ID: pm.P1, BestOf: pm.BestOf, Bye: pm.Bye, Rematch: pm.Rematch,
			// Keep insertion order stable across databases with coarse clocks.
			CreatedAt: now.Add(time.Duration(i) * time.Microsecond),
		}
		if pm.Bye {
			m.P1Score = WinThreshold(pm.BestOf)
			w := pm.P1
			m.WinnerID = &w
		} else {
			p2 := pm.P2
			m.P2ID = &p2
		}
		rows = append(rows, m)
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Create(&rows).Error
}

// SetResult records the scores of a match of the current round.
func (s *Service) SetResult(ctx context.Context, subject, id, mid string, in ResultInput) (*TournamentDTO, error) {
	return s.mutate(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if l.t.Status != StatusInProgress {
			return errConflict("results can only be entered while the tournament is in progress")
		}
		var m *Match
		for i := range l.matches {
			if l.matches[i].ID == mid {
				m = &l.matches[i]
			}
		}
		if m == nil {
			return &Error{http.StatusNotFound, "match not found"}
		}
		if m.Round != l.currentRound() {
			return errConflict("only matches of the current round can be edited")
		}
		if m.Bye || m.Forfeit {
			return errConflict("byes and forfeits cannot be edited")
		}
		if !ValidScores(m.BestOf, in.P1Score, in.P2Score) {
			return errValidation(fmt.Sprintf("scores %d-%d do not form a valid best-of-%d result", in.P1Score, in.P2Score, m.BestOf))
		}
		m.P1Score, m.P2Score = in.P1Score, in.P2Score
		winner := m.P1ID
		if in.P2Score > in.P1Score {
			winner = *m.P2ID
		}
		m.WinnerID = &winner
		return tx.Model(&Match{}).Where("id = ?", mid).
			Updates(map[string]any{"p1_score": m.P1Score, "p2_score": m.P2Score, "winner_id": winner}).Error
	})
}

// Drop marks a participant as dropped and forfeits their unplayed current match.
func (s *Service) Drop(ctx context.Context, subject, id, pid string) (*TournamentDTO, error) {
	return s.mutate(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if l.t.Status != StatusInProgress {
			return errConflict("participants can only be dropped while the tournament is in progress")
		}
		var p *Participant
		for i := range l.participants {
			if l.participants[i].ID == pid {
				p = &l.participants[i]
			}
		}
		if p == nil {
			return &Error{http.StatusNotFound, "participant not found"}
		}
		if p.Dropped {
			return errConflict("participant already dropped")
		}
		if err := tx.Model(&Participant{}).Where("id = ?", pid).Update("dropped", true).Error; err != nil {
			return err
		}
		cur := l.currentRound()
		for _, m := range l.matches {
			if m.Round != cur || m.Resolved() || m.Bye || m.P2ID == nil {
				continue
			}
			if m.P1ID != pid && *m.P2ID != pid {
				continue
			}
			winner, p1, p2 := *m.P2ID, 0, WinThreshold(m.BestOf)
			if *m.P2ID == pid {
				winner, p1, p2 = m.P1ID, WinThreshold(m.BestOf), 0
			}
			if err := tx.Model(&Match{}).Where("id = ?", m.ID).
				Updates(map[string]any{"p1_score": p1, "p2_score": p2, "winner_id": winner, "forfeit": true}).Error; err != nil {
				return err
			}
		}
		s.log.Info("participant dropped", "tournament_id", id, "participant_id", pid)
		return nil
	})
}

// CloseRound generates the next round, or completes the tournament.
func (s *Service) CloseRound(ctx context.Context, subject, id string) (*TournamentDTO, error) {
	return s.mutate(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if l.t.Status != StatusInProgress {
			return errConflict("no round to close")
		}
		cur := l.currentRound()
		for _, m := range l.matches {
			if m.Round == cur && !m.Resolved() {
				return errConflict("every match of the current round needs a result")
			}
		}
		standings, _ := ComputeStandings(l.participants, l.matches)
		planned := PairRound(l.pairingConfig(), standings)
		if cur >= l.t.MaxRounds() || len(planned) == 0 {
			l.t.Status = StatusCompleted
			s.log.Info("tournament completed", "tournament_id", id, "rounds", cur)
			return tx.Save(&l.t).Error
		}
		return s.insertRound(tx, l, cur+1, planned)
	})
}

// UndoRound removes the latest round when it has no result. Only allowed
// while in progress; a completed tournament is read-only.
func (s *Service) UndoRound(ctx context.Context, subject, id string) (*TournamentDTO, error) {
	return s.mutate(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if l.t.Status != StatusInProgress {
			return errConflict("rounds can only be undone while the tournament is in progress")
		}
		cur := l.currentRound()
		if cur <= 1 {
			return errConflict("the first round cannot be undone")
		}
		for _, m := range l.matches {
			if m.Round == cur && !m.Bye && !m.Forfeit && m.Resolved() {
				return errConflict("the latest round already has results")
			}
		}
		return tx.Where("tournament_id = ? AND round = ?", id, cur).Delete(&Match{}).Error
	})
}
