package swiss

import (
	"sort"
)

// PairingConfig is the subset of the tournament configuration the pairer needs.
type PairingConfig struct {
	WinsToAdvance     int
	LossesToEliminate int
	BestOf            int
	DecidersBestOf    int
	AllowRematches    bool
}

// Standing is the derived state of one participant, input of the pairer and
// of the standings sort.
type Standing struct {
	ID        string
	Seed      int
	Wins      int
	Losses    int
	Buchholz  int
	Dropped   bool
	HadBye    bool
	Opponents []string // ids faced so far, byes excluded, duplicates kept
}

// Active reports whether the participant still plays matches.
func (s Standing) Active(cfg PairingConfig) bool {
	return !s.Dropped && s.Wins < cfg.WinsToAdvance && s.Losses < cfg.LossesToEliminate
}

// Status is the participant status shown in the API.
func (s Standing) Status(cfg PairingConfig) string {
	switch {
	case s.Wins >= cfg.WinsToAdvance:
		return ParticipantAdvanced
	case s.Dropped:
		return ParticipantDropped
	case s.Losses >= cfg.LossesToEliminate:
		return ParticipantEliminated
	default:
		return ParticipantActive
	}
}

// PlannedMatch is a match produced by the pairer, before it gets an id.
type PlannedMatch struct {
	P1           string
	P2           string // empty for a bye
	BucketWins   int
	BucketLosses int
	BestOf       int
	Bye          bool
	Rematch      bool
}

// WinThreshold is the number of game wins needed to win a best-of.
func WinThreshold(bestOf int) int { return bestOf/2 + 1 }

// ValidScores reports whether the scores form a valid result for the best-of.
func ValidScores(bestOf, p1, p2 int) bool {
	t := WinThreshold(bestOf)
	if p1 < 0 || p2 < 0 {
		return false
	}
	return (p1 == t && p2 < t) || (p2 == t && p1 < t)
}

// MatchBestOf applies rule 6: deciders best-of when either participant is one
// win from advancing or one loss from elimination.
func MatchBestOf(cfg PairingConfig, a, b *Standing) int {
	for _, s := range []*Standing{a, b} {
		if s == nil {
			continue
		}
		if s.Wins == cfg.WinsToAdvance-1 || s.Losses == cfg.LossesToEliminate-1 {
			return cfg.DecidersBestOf
		}
	}
	return cfg.BestOf
}

func (s *Standing) met(other *Standing) bool {
	for _, id := range s.Opponents {
		if id == other.ID {
			return true
		}
	}
	return false
}

// orderBucket sorts a bucket by Buchholz desc then seed asc. Round 1 has every
// Buchholz at zero, so the same sort yields seed order.
func orderBucket(b []*Standing) {
	sort.SliceStable(b, func(i, j int) bool {
		if b[i].Buchholz != b[j].Buchholz {
			return b[i].Buchholz > b[j].Buchholz
		}
		return b[i].Seed < b[j].Seed
	})
}

// bucketKey groups by record.
type bucketKey struct{ wins, losses int }

// PairRound builds the matches of the next round from the current standings.
// It is deterministic: the only inputs are the standings and the config.
func PairRound(cfg PairingConfig, standings []Standing) []PlannedMatch {
	buckets := map[bucketKey][]*Standing{}
	for i := range standings {
		s := &standings[i]
		if !s.Active(cfg) {
			continue
		}
		k := bucketKey{s.Wins, s.Losses}
		buckets[k] = append(buckets[k], s)
	}
	keys := make([]bucketKey, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	// Best record first: more wins, then fewer losses.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].wins != keys[j].wins {
			return keys[i].wins > keys[j].wins
		}
		return keys[i].losses < keys[j].losses
	})

	var out []PlannedMatch
	var floater *Standing
	for bi, k := range keys {
		members := buckets[k]
		orderBucket(members)
		last := bi == len(keys)-1

		// Rule 5: a floater from the better bucket is paired first against the
		// highest-ordered member it has not met; it never floats twice.
		if floater != nil {
			idx := 0
			rematch := true
			for i, m := range members {
				if cfg.AllowRematches || !floater.met(m) {
					idx, rematch = i, false
					break
				}
			}
			out = append(out, newMatch(cfg, floater, members[idx], k, rematch && !cfg.AllowRematches))
			members = append(members[:idx:idx], members[idx+1:]...)
			floater = nil
		}

		if len(members)%2 == 1 {
			if last {
				// Rule 5: bye to the lowest-ordered participant without a bye yet.
				byeIdx := len(members) - 1
				for i := len(members) - 1; i >= 0; i-- {
					if !members[i].HadBye {
						byeIdx = i
						break
					}
				}
				bye := members[byeIdx]
				members = append(members[:byeIdx:byeIdx], members[byeIdx+1:]...)
				out = append(out, PlannedMatch{
					P1: bye.ID, BucketWins: k.wins, BucketLosses: k.losses,
					BestOf: MatchBestOf(cfg, bye, nil), Bye: true,
				})
			} else {
				floater = members[len(members)-1]
				members = members[:len(members)-1]
			}
		}

		out = append(out, pairEven(cfg, members, k)...)
	}
	if floater != nil {
		// Only possible when the worst bucket vanished entirely into floats,
		// which cannot happen, but never silently drop a participant.
		out = append(out, PlannedMatch{
			P1: floater.ID, BucketWins: floater.Wins, BucketLosses: floater.Losses,
			BestOf: MatchBestOf(cfg, floater, nil), Bye: true,
		})
	}
	return out
}

// pairEven applies rule 4: top half vs bottom half, swapping the bottom
// candidate forward when the proposed pair is a rematch.
func pairEven(cfg PairingConfig, members []*Standing, k bucketKey) []PlannedMatch {
	half := len(members) / 2
	top := members[:half]
	pool := append([]*Standing(nil), members[half:]...)
	out := make([]PlannedMatch, 0, half)
	for _, a := range top {
		idx := 0
		rematch := true
		for i, b := range pool {
			if cfg.AllowRematches || !a.met(b) {
				idx, rematch = i, false
				break
			}
		}
		out = append(out, newMatch(cfg, a, pool[idx], k, rematch && !cfg.AllowRematches))
		pool = append(pool[:idx:idx], pool[idx+1:]...)
	}
	return out
}

func newMatch(cfg PairingConfig, a, b *Standing, k bucketKey, rematch bool) PlannedMatch {
	return PlannedMatch{
		P1: a.ID, P2: b.ID,
		BucketWins: k.wins, BucketLosses: k.losses,
		BestOf:  MatchBestOf(cfg, a, b),
		Rematch: rematch,
	}
}

// ---- Derived records -------------------------------------------------------

// HeadToHead maps "loserID|winnerID" pairs to true for the latest meeting.
type HeadToHead map[[2]string]bool

// ComputeStandings derives wins, losses, Buchholz, opponents and byes from the
// matches. Participants must be ordered by seed. Matches must be ordered by
// round then creation, so the latest meeting wins the head-to-head.
func ComputeStandings(participants []Participant, matches []Match) ([]Standing, HeadToHead) {
	index := map[string]int{}
	out := make([]Standing, len(participants))
	for i, p := range participants {
		index[p.ID] = i
		out[i] = Standing{ID: p.ID, Seed: p.Seed, Dropped: p.Dropped}
	}
	h2h := HeadToHead{}
	for _, m := range matches {
		i, ok := index[m.P1ID]
		if !ok {
			continue
		}
		if m.Bye {
			out[i].HadBye = true
			out[i].Wins++
			continue
		}
		if m.P2ID == nil {
			continue
		}
		j, ok := index[*m.P2ID]
		if !ok {
			continue
		}
		out[i].Opponents = append(out[i].Opponents, *m.P2ID)
		out[j].Opponents = append(out[j].Opponents, m.P1ID)
		if !m.Resolved() {
			continue
		}
		if *m.WinnerID == m.P1ID {
			out[i].Wins++
			out[j].Losses++
			h2h[[2]string{m.P1ID, *m.P2ID}] = true
			delete(h2h, [2]string{*m.P2ID, m.P1ID})
		} else {
			out[j].Wins++
			out[i].Losses++
			h2h[[2]string{*m.P2ID, m.P1ID}] = true
			delete(h2h, [2]string{m.P1ID, *m.P2ID})
		}
	}
	for i := range out {
		for _, opp := range out[i].Opponents {
			out[i].Buchholz += out[index[opp]].Wins
		}
	}
	return out, h2h
}

// SortStandings orders ids for the standings table: wins desc, losses asc,
// Buchholz desc, head-to-head, seed asc.
func SortStandings(standings []Standing, h2h HeadToHead) []string {
	s := append([]Standing(nil), standings...)
	sort.SliceStable(s, func(i, j int) bool {
		a, b := s[i], s[j]
		if a.Wins != b.Wins {
			return a.Wins > b.Wins
		}
		if a.Losses != b.Losses {
			return a.Losses < b.Losses
		}
		if a.Buchholz != b.Buchholz {
			return a.Buchholz > b.Buchholz
		}
		if h2h[[2]string{a.ID, b.ID}] {
			return true
		}
		if h2h[[2]string{b.ID, a.ID}] {
			return false
		}
		return a.Seed < b.Seed
	})
	ids := make([]string, len(s))
	for i, st := range s {
		ids[i] = st.ID
	}
	return ids
}
