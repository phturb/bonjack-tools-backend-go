package swiss

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBracketOrder(t *testing.T) {
	assert.Equal(t, 2, BracketSize(2))
	assert.Equal(t, 4, BracketSize(3))
	assert.Equal(t, 8, BracketSize(5))
	assert.Equal(t, 8, BracketSize(8))
	assert.Equal(t, []int{1, 2}, BracketOrder(2))
	assert.Equal(t, []int{1, 4, 2, 3}, BracketOrder(4))
	assert.Equal(t, []int{1, 8, 4, 5, 2, 7, 3, 6}, BracketOrder(8))
	assert.Equal(t, []int{1, 16, 8, 9, 4, 13, 5, 12, 2, 15, 7, 10, 3, 14, 6, 11}, BracketOrder(16))
}

func TestPlanPlayoffRound1(t *testing.T) {
	assert.Nil(t, PlanPlayoffRound1([]string{"a"}, 3))

	// Five qualifiers on an 8-bracket: seeds 1, 2 and 3 get byes.
	planned := PlanPlayoffRound1([]string{"s1", "s2", "s3", "s4", "s5"}, 3)
	require.Len(t, planned, 4)
	assert.Equal(t, PlannedMatch{P1: "s1", Bye: true, BestOf: 3}, planned[0])
	assert.Equal(t, PlannedMatch{P1: "s4", P2: "s5", BestOf: 3}, planned[1])
	assert.Equal(t, PlannedMatch{P1: "s2", Bye: true, BestOf: 3}, planned[2])
	assert.Equal(t, PlannedMatch{P1: "s3", Bye: true, BestOf: 3}, planned[3])

	// Exactly four: no bye, 1v4 and 2v3.
	planned = PlanPlayoffRound1([]string{"s1", "s2", "s3", "s4"}, 1)
	require.Len(t, planned, 2)
	assert.Equal(t, PlannedMatch{P1: "s1", P2: "s4", BestOf: 1}, planned[0])
	assert.Equal(t, PlannedMatch{P1: "s2", P2: "s3", BestOf: 1}, planned[1])
}

// completeSwiss plays a tournament to the end of its Swiss stage, p1 winning everything.
func completeSwiss(t *testing.T, svc *Service, n int, in ConfigInput) *TournamentDTO {
	t.Helper()
	tr := newStarted(t, svc, n, in)
	for tr.Status == StatusInProgress {
		tr = winAll(t, svc, tr)
		var err error
		tr, err = svc.CloseRound(context.Background(), owner, tr.ID)
		require.NoError(t, err)
	}
	require.Equal(t, StatusCompleted, tr.Status)
	return tr
}

func advanced(tr *TournamentDTO) []string {
	out := []string{}
	for _, id := range tr.Standings {
		for _, p := range tr.Participants {
			if p.ID == id && p.Status == ParticipantAdvanced {
				out = append(out, id)
			}
		}
	}
	return out
}

func TestPlayoffsLifecycle(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	tr := completeSwiss(t, svc, 8, ConfigInput{WinsToAdvance: ptr(2), LossesToEliminate: ptr(2), BestOf: ptr(1), PlayoffsBestOf: ptr(3)})
	q := advanced(tr)
	require.GreaterOrEqual(t, len(q), 2)
	require.Nil(t, tr.Playoffs)

	// Refusals before start.
	_, err := svc.StartPlayoffs(ctx, "intruder", tr.ID)
	assert.Equal(t, 403, statusOf(t, err))
	_, err = svc.CloseRound(ctx, owner, tr.ID)
	assert.Equal(t, 409, statusOf(t, err), "nothing to close while completed")

	// playoffsBestOf may still change while completed, nothing else.
	tr, err = svc.Update(ctx, owner, tr.ID, ConfigInput{PlayoffsBestOf: ptr(5)})
	require.NoError(t, err)
	assert.Equal(t, 5, tr.Config.PlayoffsBestOf)
	_, err = svc.Update(ctx, owner, tr.ID, ConfigInput{BestOf: ptr(3)})
	assert.Equal(t, 409, statusOf(t, err))

	tr, err = svc.StartPlayoffs(ctx, owner, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPlayoffs, tr.Status)
	require.NotNil(t, tr.Playoffs)
	assert.Equal(t, 5, tr.Playoffs.BestOf)
	assert.Equal(t, BracketSize(len(q)), tr.Playoffs.Size)
	assert.Equal(t, 1, tr.Playoffs.CurrentRound)
	assert.Len(t, tr.Playoffs.Rounds, 1)
	assert.Len(t, tr.Playoffs.Rounds[0].Matches, tr.Playoffs.Size/2)
	assert.Equal(t, q[0], tr.Playoffs.Rounds[0].Matches[0].P1, "best seed in slot 1")
	for _, m := range tr.Playoffs.Rounds[0].Matches {
		assert.Equal(t, 5, m.BestOf)
	}
	assert.Len(t, tr.Rounds, tr.CurrentRound, "swiss rounds untouched")
	swissStandings := tr.Standings

	// Second start refused, playoffsBestOf frozen now, Swiss match not editable.
	_, err = svc.StartPlayoffs(ctx, owner, tr.ID)
	assert.Equal(t, 409, statusOf(t, err))
	_, err = svc.Update(ctx, owner, tr.ID, ConfigInput{PlayoffsBestOf: ptr(1)})
	assert.Equal(t, 409, statusOf(t, err))
	_, err = svc.SetResult(ctx, owner, tr.ID, tr.Rounds[0].Matches[0].ID, ResultInput{P1Score: 1})
	assert.Equal(t, 404, statusOf(t, err), "swiss matches are not in the playoff stage")

	// Undo round 1 cancels the playoffs; start again.
	tr, err = svc.UndoRound(ctx, owner, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, tr.Status)
	assert.Nil(t, tr.Playoffs)
	tr, err = svc.StartPlayoffs(ctx, owner, tr.ID)
	require.NoError(t, err)

	// Play every round: the better seed (p1) wins.
	for tr.Status == StatusPlayoffs {
		cur := tr.Playoffs.Rounds[tr.Playoffs.CurrentRound-1]
		_, err = svc.CloseRound(ctx, owner, tr.ID)
		if len(cur.Matches) > 0 && cur.Matches[len(cur.Matches)-1].Winner == nil {
			assert.Equal(t, 409, statusOf(t, err), "unresolved matches block close")
		}
		for _, m := range cur.Matches {
			if m.Winner != nil {
				continue
			}
			tr, err = svc.SetResult(ctx, owner, tr.ID, m.ID, ResultInput{P1Score: WinThreshold(m.BestOf)})
			require.NoError(t, err)
		}
		if tr.Playoffs.CurrentRound > 1 {
			_, err = svc.UndoRound(ctx, owner, tr.ID)
			assert.Equal(t, 409, statusOf(t, err), "round with results cannot be undone")
		}
		tr, err = svc.CloseRound(ctx, owner, tr.ID)
		require.NoError(t, err)
	}
	assert.Equal(t, StatusFinished, tr.Status)
	require.NotNil(t, tr.Playoffs.Champion)
	assert.Equal(t, q[0], *tr.Playoffs.Champion, "top seed wins when p1 always wins")
	final := tr.Playoffs.Rounds[len(tr.Playoffs.Rounds)-1]
	assert.Len(t, final.Matches, 1)
	assert.True(t, final.Closed)
	assert.Equal(t, swissStandings, tr.Standings, "playoff results do not change the Swiss standings")

	// Finished is read-only.
	_, err = svc.UndoRound(ctx, owner, tr.ID)
	assert.Equal(t, 409, statusOf(t, err))
	_, err = svc.SetResult(ctx, owner, tr.ID, final.Matches[0].ID, ResultInput{P2Score: WinThreshold(final.Matches[0].BestOf)})
	assert.Equal(t, 409, statusOf(t, err))
	_, err = svc.CloseRound(ctx, owner, tr.ID)
	assert.Equal(t, 409, statusOf(t, err))

	// Delete removes both stages.
	require.NoError(t, svc.Delete(ctx, owner, tr.ID))
	_, err = svc.Get(ctx, tr.ID)
	assert.Equal(t, 404, statusOf(t, err))
}

func TestPlayoffsNeedTwoQualifiers(t *testing.T) {
	svc := newTestService(t)
	// Two players, one win to advance: a single qualifier.
	tr := completeSwiss(t, svc, 2, ConfigInput{WinsToAdvance: ptr(1), LossesToEliminate: ptr(1)})
	require.Len(t, advanced(tr), 1)
	_, err := svc.StartPlayoffs(context.Background(), owner, tr.ID)
	assert.Equal(t, 422, statusOf(t, err))
}
