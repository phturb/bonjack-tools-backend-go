package swiss

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cfg33bo1() PairingConfig {
	return PairingConfig{WinsToAdvance: 3, LossesToEliminate: 3, BestOf: 1, DecidersBestOf: 3}
}

func seeds(n int) []Standing {
	out := make([]Standing, n)
	for i := range out {
		out[i] = Standing{ID: fmt.Sprintf("p%d", i+1), Seed: i + 1}
	}
	return out
}

func pairKey(m PlannedMatch) string { return m.P1 + "-" + m.P2 }

func TestPairRound_Buckets(t *testing.T) {
	tests := []struct {
		name      string
		standings []Standing
		want      []string // "p1-p5" pairs, "p9-" for a bye, in output order
		byes      int
	}{
		{
			name:      "even bucket top half vs bottom half",
			standings: seeds(8),
			want:      []string{"p1-p5", "p2-p6", "p3-p7", "p4-p8"},
		},
		{
			name: "odd bucket floats lowest down and pairs it first below",
			standings: []Standing{
				{ID: "a1", Seed: 1, Wins: 1}, {ID: "a2", Seed: 2, Wins: 1}, {ID: "a3", Seed: 3, Wins: 1},
				{ID: "b1", Seed: 4, Losses: 1}, {ID: "b2", Seed: 5, Losses: 1}, {ID: "b3", Seed: 6, Losses: 1},
			},
			want: []string{"a1-a2", "a3-b1", "b2-b3"},
		},
		{
			name: "buchholz orders inside a bucket before seed",
			standings: []Standing{
				{ID: "p1", Seed: 1, Wins: 1, Buchholz: 0}, {ID: "p2", Seed: 2, Wins: 1, Buchholz: 1},
				{ID: "p3", Seed: 3, Wins: 1, Buchholz: 1}, {ID: "p4", Seed: 4, Wins: 1, Buchholz: 0},
			},
			want: []string{"p2-p1", "p3-p4"},
		},
		{
			name:      "odd worst bucket gives a bye to the lowest",
			standings: seeds(5),
			want:      []string{"p5-", "p1-p3", "p2-p4"},
			byes:      1,
		},
		{
			name: "bye skips a participant that already had one",
			standings: []Standing{
				{ID: "p1", Seed: 1}, {ID: "p2", Seed: 2}, {ID: "p3", Seed: 3, HadBye: true},
			},
			want: []string{"p2-", "p1-p3"},
			byes: 1,
		},
		{
			name: "advanced, eliminated and dropped are not paired",
			standings: []Standing{
				{ID: "adv", Seed: 1, Wins: 3}, {ID: "elim", Seed: 2, Losses: 3}, {ID: "drop", Seed: 3, Dropped: true},
				{ID: "p4", Seed: 4}, {ID: "p5", Seed: 5},
			},
			want: []string{"p4-p5"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PairRound(cfg33bo1(), tc.standings)
			keys := make([]string, len(got))
			byes := 0
			for i, m := range got {
				keys[i] = pairKey(m)
				if m.Bye {
					byes++
					assert.Empty(t, m.P2)
				}
			}
			assert.Equal(t, tc.want, keys)
			assert.Equal(t, tc.byes, byes)
		})
	}
}

func TestPairRound_Rematches(t *testing.T) {
	tests := []struct {
		name      string
		allow     bool
		standings []Standing
		want      []string
		rematches []bool
	}{
		{
			name: "rematch avoided by swapping the bottom candidate",
			standings: []Standing{
				{ID: "p1", Seed: 1, Opponents: []string{"p3"}}, {ID: "p2", Seed: 2, Opponents: []string{"p4"}},
				{ID: "p3", Seed: 3, Opponents: []string{"p1"}}, {ID: "p4", Seed: 4, Opponents: []string{"p2"}},
			},
			want:      []string{"p1-p4", "p2-p3"},
			rematches: []bool{false, false},
		},
		{
			name: "rematch forced and flagged when unavoidable",
			standings: []Standing{
				{ID: "p1", Seed: 1, Opponents: []string{"p2"}}, {ID: "p2", Seed: 2, Opponents: []string{"p1"}},
			},
			want:      []string{"p1-p2"},
			rematches: []bool{true},
		},
		{
			name:  "rematch not flagged when allowed",
			allow: true,
			standings: []Standing{
				{ID: "p1", Seed: 1, Opponents: []string{"p2"}}, {ID: "p2", Seed: 2, Opponents: []string{"p1"}},
			},
			want:      []string{"p1-p2"},
			rematches: []bool{false},
		},
		{
			name: "floater avoids a rematch in the lower bucket",
			standings: []Standing{
				{ID: "a1", Seed: 1, Wins: 1}, {ID: "a2", Seed: 2, Wins: 1}, {ID: "a3", Seed: 3, Wins: 1, Opponents: []string{"b1"}},
				{ID: "b1", Seed: 4, Losses: 1, Opponents: []string{"a3"}}, {ID: "b2", Seed: 5, Losses: 1}, {ID: "b3", Seed: 6, Losses: 1},
			},
			want:      []string{"a1-a2", "a3-b2", "b1-b3"},
			rematches: []bool{false, false, false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg33bo1()
			c.AllowRematches = tc.allow
			got := PairRound(c, tc.standings)
			require.Len(t, got, len(tc.want))
			for i, m := range got {
				assert.Equal(t, tc.want[i], pairKey(m))
				assert.Equal(t, tc.rematches[i], m.Rematch, "rematch flag of %s", tc.want[i])
			}
		})
	}
}

func TestMatchBestOf(t *testing.T) {
	c := PairingConfig{WinsToAdvance: 3, LossesToEliminate: 3, BestOf: 1, DecidersBestOf: 5}
	tests := []struct {
		name string
		a, b Standing
		want int
	}{
		{"fresh participants play bestOf", Standing{}, Standing{}, 1},
		{"one win from advancing", Standing{Wins: 2}, Standing{Wins: 2}, 5},
		{"one loss from elimination", Standing{Losses: 2}, Standing{Losses: 2}, 5},
		{"either participant triggers it", Standing{Wins: 1, Losses: 1}, Standing{Wins: 2}, 5},
		{"two wins with W=5 is not a decider", Standing{Wins: 2}, Standing{Wins: 2}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cc := c
			if tc.name == "two wins with W=5 is not a decider" {
				cc.WinsToAdvance = 5
			}
			a, b := tc.a, tc.b
			assert.Equal(t, tc.want, MatchBestOf(cc, &a, &b))
		})
	}
}

func TestValidScores(t *testing.T) {
	tests := []struct {
		bestOf, p1, p2 int
		want           bool
	}{
		{1, 1, 0, true}, {1, 0, 1, true}, {1, 1, 1, false}, {1, 2, 0, false}, {1, 0, 0, false},
		{3, 2, 0, true}, {3, 2, 1, true}, {3, 1, 2, true}, {3, 2, 2, false}, {3, 1, 0, false}, {3, -1, 2, false},
		{5, 3, 2, true}, {5, 3, 3, false}, {5, 2, 1, false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, ValidScores(tc.bestOf, tc.p1, tc.p2), "bo%d %d-%d", tc.bestOf, tc.p1, tc.p2)
	}
}

// simulate runs a whole tournament in memory, the higher seed winning every
// match, and returns the matches per round plus the final standings.
func simulate(t *testing.T, c PairingConfig, n int) ([]int, []Standing) {
	parts := make([]Participant, n)
	for i := range parts {
		parts[i] = Participant{ID: fmt.Sprintf("p%d", i+1), Seed: i + 1}
	}
	seedOf := map[string]int{}
	for _, p := range parts {
		seedOf[p.ID] = p.Seed
	}
	var matches []Match
	var counts []int
	maxRounds := c.WinsToAdvance + c.LossesToEliminate - 1
	for round := 1; round <= maxRounds; round++ {
		standings, _ := ComputeStandings(parts, matches)
		planned := PairRound(c, standings)
		if len(planned) == 0 {
			break
		}
		counts = append(counts, len(planned))
		for i, pm := range planned {
			m := Match{ID: fmt.Sprintf("r%dm%d", round, i), Round: round, P1ID: pm.P1, BestOf: pm.BestOf, Bye: pm.Bye, Rematch: pm.Rematch}
			if pm.Bye {
				w := pm.P1
				m.WinnerID = &w
				m.P1Score = WinThreshold(pm.BestOf)
			} else {
				p2 := pm.P2
				m.P2ID = &p2
				w := pm.P1
				if seedOf[pm.P2] < seedOf[pm.P1] {
					w = pm.P2
				}
				m.WinnerID = &w
			}
			matches = append(matches, m)
		}
	}
	standings, _ := ComputeStandings(parts, matches)
	return counts, standings
}

func TestSimulate16Players(t *testing.T) {
	c := cfg33bo1()
	counts, standings := simulate(t, c, 16)
	assert.Equal(t, []int{8, 8, 8, 6, 3}, counts)
	status := map[string]int{}
	for _, s := range standings {
		status[s.Status(c)]++
	}
	assert.Equal(t, 8, status[ParticipantAdvanced])
	assert.Equal(t, 8, status[ParticipantEliminated])
	assert.Equal(t, 0, status[ParticipantActive])
}

func TestSimulate_ByeAtMostOncePerParticipant(t *testing.T) {
	for _, n := range []int{3, 5, 7, 9, 11, 13} {
		t.Run(fmt.Sprintf("%d players", n), func(t *testing.T) {
			c := PairingConfig{WinsToAdvance: 3, LossesToEliminate: 3, BestOf: 1, DecidersBestOf: 1}
			parts := make([]Participant, n)
			for i := range parts {
				parts[i] = Participant{ID: fmt.Sprintf("p%d", i+1), Seed: i + 1}
			}
			var matches []Match
			for round := 1; round <= c.WinsToAdvance+c.LossesToEliminate-1; round++ {
				standings, _ := ComputeStandings(parts, matches)
				byID := map[string]Standing{}
				for _, s := range standings {
					byID[s.ID] = s
				}
				for i, pm := range PairRound(c, standings) {
					m := Match{ID: fmt.Sprintf("r%dm%d", round, i), Round: round, P1ID: pm.P1, BestOf: pm.BestOf, Bye: pm.Bye}
					w := pm.P1
					if pm.Bye {
						if byID[pm.P1].HadBye {
							// A second bye is only legal when every active
							// participant of that bucket already had one.
							for _, s := range standings {
								if s.Active(c) && s.Wins == pm.BucketWins && s.Losses == pm.BucketLosses {
									assert.True(t, s.HadBye, "round %d: %s got a second bye while %s never had one", round, pm.P1, s.ID)
								}
							}
						}
					} else {
						p2 := pm.P2
						m.P2ID = &p2
						if parts[seedIndex(pm.P2)].Seed < parts[seedIndex(pm.P1)].Seed {
							w = pm.P2
						}
					}
					m.WinnerID = &w
					matches = append(matches, m)
				}
			}
		})
	}
}

func seedIndex(id string) int {
	var n int
	fmt.Sscanf(id, "p%d", &n)
	return n - 1
}

func TestComputeStandingsAndSort(t *testing.T) {
	parts := []Participant{{ID: "a", Seed: 1}, {ID: "b", Seed: 2}, {ID: "c", Seed: 3}, {ID: "d", Seed: 4}}
	b, d := "b", "d"
	a, c := "a", "c"
	matches := []Match{
		{ID: "1", Round: 1, P1ID: "a", P2ID: &b, WinnerID: &a},
		{ID: "2", Round: 1, P1ID: "c", P2ID: &d, WinnerID: &d},
		{ID: "3", Round: 2, P1ID: "a", P2ID: &d, WinnerID: &d},
		{ID: "4", Round: 2, P1ID: "b", P2ID: &c, WinnerID: &c},
	}
	standings, h2h := ComputeStandings(parts, matches)
	byID := map[string]Standing{}
	for _, s := range standings {
		byID[s.ID] = s
	}
	assert.Equal(t, 2, byID["d"].Wins)
	assert.Equal(t, 1, byID["a"].Wins)
	assert.Equal(t, 1, byID["a"].Losses)
	// a faced b (0 wins) and d (2 wins)
	assert.Equal(t, 2, byID["a"].Buchholz)
	// c faced d (2 wins) and b (0 wins)
	assert.Equal(t, 2, byID["c"].Buchholz)
	assert.True(t, h2h[[2]string{"d", "a"}])

	// d first; a and c are both 1-1 with equal Buchholz and never met, so seed decides
	assert.Equal(t, []string{"d", "a", "c", "b"}, SortStandings(standings, h2h))
}
