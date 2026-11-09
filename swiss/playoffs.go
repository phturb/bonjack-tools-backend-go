package swiss

import (
	"context"

	"gorm.io/gorm"
)

// Single-elimination playoffs played after the Swiss stage among the
// participants that advanced, seeded by the Swiss standings.
//
// The bracket size is the smallest power of two that fits the qualifiers; the
// top seeds receive byes in round 1 when the field is smaller. Round 1 uses
// the standard seed layout (1 v n, then 4 v 5, 2 v 7, 3 v 6 for eight), so the
// winners of consecutive matches meet in the next round and the two best seeds
// can only meet in the final. Later rounds are generated when the previous one
// is closed, the same lifecycle as the Swiss rounds.

// BracketSize returns the smallest power of two that holds n players.
func BracketSize(n int) int {
	size := 1
	for size < n {
		size *= 2
	}
	return size
}

// BracketOrder returns the seeds of a bracket of the given size (a power of
// two) in slot order; consecutive pairs are the round-1 matches, e.g. size 8
// gives [1 8 4 5 2 7 3 6].
func BracketOrder(size int) []int {
	order := []int{1}
	for cur := 2; cur <= size; cur *= 2 {
		next := make([]int, 0, cur)
		for _, s := range order {
			next = append(next, s, cur+1-s)
		}
		order = next
	}
	return order
}

// PlanPlayoffRound1 lays the qualifiers, given in seed order (best first),
// on the bracket. A missing opponent is a bye for the better seed.
func PlanPlayoffRound1(qualifiers []string, bestOf int) []PlannedMatch {
	if len(qualifiers) < 2 {
		return nil
	}
	size := BracketSize(len(qualifiers))
	order := BracketOrder(size)
	planned := make([]PlannedMatch, 0, size/2)
	for i := 0; i < size; i += 2 {
		a, b := order[i], order[i+1] // a < b always, so a is a real qualifier
		pm := PlannedMatch{P1: qualifiers[a-1], BestOf: bestOf}
		if b <= len(qualifiers) {
			pm.P2 = qualifiers[b-1]
		} else {
			pm.Bye = true
		}
		planned = append(planned, pm)
	}
	return planned
}

// PlanNextPlayoffRound pairs the winners of the previous round in slot
// order. It returns nil after the final (a single match).
func PlanNextPlayoffRound(previous []Match, bestOf int) []PlannedMatch {
	if len(previous) < 2 {
		return nil
	}
	planned := make([]PlannedMatch, 0, len(previous)/2)
	for i := 0; i+1 < len(previous); i += 2 {
		planned = append(planned, PlannedMatch{P1: *previous[i].WinnerID, P2: *previous[i+1].WinnerID, BestOf: bestOf})
	}
	return planned
}

// qualifiers returns the advanced, non-dropped participants in standings order.
func (l *loaded) qualifiers() []string {
	cfg := l.pairingConfig()
	standings, h2h := ComputeStandings(l.participants, l.matches)
	byID := map[string]Standing{}
	for _, st := range standings {
		byID[st.ID] = st
	}
	out := make([]string, 0, len(standings))
	for _, id := range SortStandings(standings, h2h) {
		if st := byID[id]; st.Status(cfg) == ParticipantAdvanced && !st.Dropped {
			out = append(out, id)
		}
	}
	return out
}

// StartPlayoffs generates playoff round 1 among the advanced participants of
// a completed Swiss stage.
func (s *Service) StartPlayoffs(ctx context.Context, subject, id string) (*TournamentDTO, error) {
	return s.mutate(ctx, subject, id, func(tx *gorm.DB, l *loaded) error {
		if l.t.Status != StatusCompleted {
			return errConflict("playoffs can only start once the Swiss stage is completed")
		}
		q := l.qualifiers()
		if len(q) < 2 {
			return errValidation("at least two advanced participants are required for playoffs")
		}
		if err := s.insertRound(tx, l, StagePlayoffs, 1, PlanPlayoffRound1(q, l.t.PlayoffsBestOf)); err != nil {
			return err
		}
		l.t.Status = StatusPlayoffs
		s.log.Info("playoffs started", "tournament_id", id, "qualifiers", len(q))
		return tx.Save(&l.t).Error
	})
}

// closePlayoffRound generates the next playoff round or finishes the tournament.
func (s *Service) closePlayoffRound(tx *gorm.DB, l *loaded, cur int) error {
	current := make([]Match, 0)
	for _, m := range l.playoffs {
		if m.Round == cur {
			current = append(current, m)
		}
	}
	planned := PlanNextPlayoffRound(current, l.t.PlayoffsBestOf)
	if len(planned) == 0 {
		l.t.Status = StatusFinished
		s.log.Info("tournament finished", "tournament_id", l.t.ID, "playoff_rounds", cur)
		return tx.Save(&l.t).Error
	}
	return s.insertRound(tx, l, StagePlayoffs, cur+1, planned)
}
