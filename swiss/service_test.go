package swiss

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// testDeps implements internal.Dependencies over an in-memory sqlite database.
type testDeps struct{ db *gorm.DB }

func (d *testDeps) Database(ctx context.Context) *gorm.DB { return d.db.WithContext(ctx) }
func (d *testDeps) Cron(context.Context) *cron.Cron       { return cron.New() }

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// ":memory:" is per connection; keep a single one so transactions see the schema.
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(Models()...))
	return NewService(&testDeps{db: db})
}

const owner = "oauth2|discord|1"

func ptr[T any](v T) *T { return &v }

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var e *Error
	require.True(t, errors.As(err, &e), "expected a swiss.Error, got %v", err)
	return e.Status
}

func names(n int) []ParticipantInput {
	out := make([]ParticipantInput, n)
	for i := range out {
		out[i] = ParticipantInput{Name: fmt.Sprintf("Player %d", i+1), Seed: i + 1}
	}
	return out
}

// newStarted creates a tournament with n participants and starts it.
func newStarted(t *testing.T, svc *Service, n int, in ConfigInput) *TournamentDTO {
	t.Helper()
	ctx := context.Background()
	if in.Name == nil {
		in.Name = ptr("Friday Swiss")
	}
	tr, err := svc.Create(ctx, owner, in)
	require.NoError(t, err)
	_, err = svc.ReplaceParticipants(ctx, owner, tr.ID, names(n))
	require.NoError(t, err)
	tr, err = svc.Start(ctx, owner, tr.ID)
	require.NoError(t, err)
	return tr
}

func currentMatches(tr *TournamentDTO) []MatchDTO {
	return tr.Rounds[tr.CurrentRound-1].Matches
}

// winAll gives every unresolved match of the current round to p1.
func winAll(t *testing.T, svc *Service, tr *TournamentDTO) *TournamentDTO {
	t.Helper()
	ctx := context.Background()
	for _, m := range currentMatches(tr) {
		if m.Winner != nil {
			continue
		}
		var err error
		tr, err = svc.SetResult(ctx, owner, tr.ID, m.ID, ResultInput{P1Score: WinThreshold(m.BestOf)})
		require.NoError(t, err)
	}
	return tr
}

func TestCreateDefaultsAndValidation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	tr, err := svc.Create(ctx, owner, ConfigInput{Name: ptr("Cup")})
	require.NoError(t, err)
	assert.Len(t, tr.ID, 10)
	assert.Equal(t, StatusDraft, tr.Status)
	assert.Equal(t, owner, tr.OwnerSub)
	assert.Equal(t, ConfigDTO{WinsToAdvance: 3, LossesToEliminate: 3, BestOf: 1, DecidersBestOf: 1, Seeding: SeedingManual, Visibility: VisibilityPublic}, tr.Config)
	assert.Equal(t, 0, tr.CurrentRound)
	assert.Empty(t, tr.Rounds)
	assert.Empty(t, tr.Participants)

	tr, err = svc.Create(ctx, owner, ConfigInput{Name: ptr("Cup"), BestOf: ptr(3)})
	require.NoError(t, err)
	assert.Equal(t, 3, tr.Config.DecidersBestOf, "decidersBestOf defaults to bestOf")

	tests := []struct {
		name string
		in   ConfigInput
	}{
		{"missing name", ConfigInput{}},
		{"long name", ConfigInput{Name: ptr(string(make([]byte, 81)))}},
		{"wins out of range", ConfigInput{Name: ptr("x"), WinsToAdvance: ptr(6)}},
		{"losses out of range", ConfigInput{Name: ptr("x"), LossesToEliminate: ptr(0)}},
		{"even best of", ConfigInput{Name: ptr("x"), BestOf: ptr(2)}},
		{"bad deciders", ConfigInput{Name: ptr("x"), DecidersBestOf: ptr(4)}},
		{"bad seeding", ConfigInput{Name: ptr("x"), Seeding: ptr("ladder")}},
		{"bad visibility", ConfigInput{Name: ptr("x"), Visibility: ptr("private")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(ctx, owner, tc.in)
			assert.Equal(t, http.StatusUnprocessableEntity, statusOf(t, err))
		})
	}
}

func TestListPublicNewestFirst(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	a, err := svc.Create(ctx, owner, ConfigInput{Name: ptr("A")})
	require.NoError(t, err)
	_, err = svc.Create(ctx, owner, ConfigInput{Name: ptr("Hidden"), Visibility: ptr(VisibilityUnlisted)})
	require.NoError(t, err)
	b, err := svc.Create(ctx, owner, ConfigInput{Name: ptr("B")})
	require.NoError(t, err)
	_, err = svc.ReplaceParticipants(ctx, owner, b.ID, names(4))
	require.NoError(t, err)

	list, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, b.ID, list[0].ID)
	assert.Equal(t, 4, list[0].Participants)
	assert.Equal(t, a.ID, list[1].ID)
}

func TestParticipantsAndStart(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	tr, err := svc.Create(ctx, owner, ConfigInput{Name: ptr("Cup")})
	require.NoError(t, err)

	_, err = svc.Start(ctx, owner, tr.ID)
	assert.Equal(t, http.StatusUnprocessableEntity, statusOf(t, err), "start with no participants")

	_, err = svc.ReplaceParticipants(ctx, owner, tr.ID, []ParticipantInput{{Name: "Alice"}, {Name: "alice "}})
	assert.Equal(t, http.StatusUnprocessableEntity, statusOf(t, err), "duplicate names")

	tr, err = svc.ReplaceParticipants(ctx, owner, tr.ID, []ParticipantInput{{Name: "Alice", Seed: 2}, {Name: "Bob", Seed: 1}, {Name: "Carol"}})
	require.NoError(t, err)
	require.Len(t, tr.Participants, 3)
	assert.Equal(t, "Bob", tr.Participants[0].Name)
	assert.Equal(t, 1, tr.Participants[0].Seed)
	assert.Equal(t, "Alice", tr.Participants[1].Name)
	assert.Equal(t, 2, tr.Participants[1].Seed)
	assert.Equal(t, "Carol", tr.Participants[2].Name)
	assert.Equal(t, 3, tr.Participants[2].Seed)

	// Re-submitting with ids keeps them; renaming works.
	bobID := tr.Participants[0].ID
	tr, err = svc.ReplaceParticipants(ctx, owner, tr.ID, []ParticipantInput{{ID: bobID, Name: "Robert", Seed: 1}, {Name: "Dan", Seed: 2}})
	require.NoError(t, err)
	require.Len(t, tr.Participants, 2)
	assert.Equal(t, bobID, tr.Participants[0].ID)
	assert.Equal(t, "Robert", tr.Participants[0].Name)

	_, err = svc.Start(ctx, "someone-else", tr.ID)
	assert.Equal(t, http.StatusForbidden, statusOf(t, err))

	tr, err = svc.Start(ctx, owner, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusInProgress, tr.Status)
	assert.Equal(t, 1, tr.CurrentRound)
	require.Len(t, tr.Rounds, 1)
	assert.False(t, tr.Rounds[0].Closed)
	require.Len(t, tr.Rounds[0].Matches, 1)
	m := tr.Rounds[0].Matches[0]
	assert.Equal(t, bobID, m.P1)
	assert.Nil(t, m.Winner)
	assert.Equal(t, BucketDTO{0, 0}, m.Bucket)

	_, err = svc.Start(ctx, owner, tr.ID)
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "start twice")
	_, err = svc.ReplaceParticipants(ctx, owner, tr.ID, names(2))
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "participants after start")
}

func TestRandomSeedingIsDeterministic(t *testing.T) {
	ctx := context.Background()
	order := func() []string {
		svc := newTestService(t)
		tr, err := svc.Create(ctx, owner, ConfigInput{Name: ptr("Cup"), Seeding: ptr(SeedingRandom)})
		require.NoError(t, err)
		// Pin the seed so two services agree.
		require.NoError(t, svc.deps.Database(ctx).Model(&Tournament{}).Where("id = ?", tr.ID).Update("rng_seed", 42).Error)
		_, err = svc.ReplaceParticipants(ctx, owner, tr.ID, names(8))
		require.NoError(t, err)
		tr, err = svc.Start(ctx, owner, tr.ID)
		require.NoError(t, err)
		out := make([]string, len(tr.Participants))
		for i, p := range tr.Participants {
			assert.Equal(t, i+1, p.Seed)
			out[i] = p.Name
		}
		return out
	}
	first, second := order(), order()
	assert.Equal(t, first, second)
	assert.NotEqual(t, []string{"Player 1", "Player 2", "Player 3", "Player 4", "Player 5", "Player 6", "Player 7", "Player 8"}, first)
}

func TestConfigUpdate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	tr, err := svc.Create(ctx, owner, ConfigInput{Name: ptr("Cup")})
	require.NoError(t, err)

	tr, err = svc.Update(ctx, owner, tr.ID, ConfigInput{BestOf: ptr(3), WinsToAdvance: ptr(2)})
	require.NoError(t, err)
	assert.Equal(t, 3, tr.Config.BestOf)
	assert.Equal(t, 2, tr.Config.WinsToAdvance)

	_, err = svc.Update(ctx, "intruder", tr.ID, ConfigInput{Name: ptr("Pwned")})
	assert.Equal(t, http.StatusForbidden, statusOf(t, err))

	_, err = svc.ReplaceParticipants(ctx, owner, tr.ID, names(2))
	require.NoError(t, err)
	_, err = svc.Start(ctx, owner, tr.ID)
	require.NoError(t, err)

	tr, err = svc.Update(ctx, owner, tr.ID, ConfigInput{Name: ptr("Renamed"), Description: ptr("desc")})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", tr.Name)
	assert.Equal(t, "desc", tr.Description)

	_, err = svc.Update(ctx, owner, tr.ID, ConfigInput{BestOf: ptr(1)})
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "config change after start")

	_, err = svc.Update(ctx, owner, "nope", ConfigInput{Name: ptr("x")})
	assert.Equal(t, http.StatusNotFound, statusOf(t, err))
}

func TestResultValidation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	tr := newStarted(t, svc, 5, ConfigInput{BestOf: ptr(3), DecidersBestOf: ptr(3)})
	require.Len(t, currentMatches(tr), 3)

	var bye, played MatchDTO
	for _, m := range currentMatches(tr) {
		if m.Bye {
			bye = m
		} else {
			played = m
		}
	}
	require.NotEmpty(t, bye.ID)
	assert.Equal(t, 2, bye.P1Score)
	assert.NotNil(t, bye.Winner)

	tests := []struct {
		name  string
		mid   string
		in    ResultInput
		sub   string
		want  int
		winOK bool
	}{
		{"tie is invalid", played.ID, ResultInput{2, 2}, owner, http.StatusUnprocessableEntity, false},
		{"incomplete series", played.ID, ResultInput{1, 0}, owner, http.StatusUnprocessableEntity, false},
		{"negative score", played.ID, ResultInput{-1, 2}, owner, http.StatusUnprocessableEntity, false},
		{"bye cannot be edited", bye.ID, ResultInput{2, 0}, owner, http.StatusConflict, false},
		{"unknown match", "zzz", ResultInput{2, 0}, owner, http.StatusNotFound, false},
		{"non owner", played.ID, ResultInput{2, 0}, "intruder", http.StatusForbidden, false},
		{"valid p2 win", played.ID, ResultInput{1, 2}, owner, 0, true},
		{"result can be corrected", played.ID, ResultInput{2, 1}, owner, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := svc.SetResult(ctx, tc.sub, tr.ID, tc.mid, tc.in)
			if !tc.winOK {
				assert.Equal(t, tc.want, statusOf(t, err))
				return
			}
			require.NoError(t, err)
			for _, m := range currentMatches(out) {
				if m.ID == tc.mid {
					require.NotNil(t, m.Winner)
					want := m.P1
					if tc.in.P2Score > tc.in.P1Score {
						want = *m.P2
					}
					assert.Equal(t, want, *m.Winner)
				}
			}
		})
	}

	// Close round 1, then the round-1 match is frozen.
	tr = winAll(t, svc, tr)
	tr, err := svc.CloseRound(ctx, owner, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, tr.CurrentRound)
	assert.True(t, tr.Rounds[0].Closed)
	_, err = svc.SetResult(ctx, owner, tr.ID, played.ID, ResultInput{2, 0})
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "wrong round")
}

func TestCloseAndUndo(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	tr := newStarted(t, svc, 4, ConfigInput{})

	_, err := svc.CloseRound(ctx, owner, tr.ID)
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "close with unresolved matches")

	_, err = svc.UndoRound(ctx, owner, tr.ID)
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "undo round 1")

	tr = winAll(t, svc, tr)
	tr, err = svc.CloseRound(ctx, owner, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, tr.CurrentRound)
	require.Len(t, tr.Rounds, 2)
	assert.Equal(t, BucketDTO{1, 0}, tr.Rounds[1].Matches[0].Bucket)
	assert.Equal(t, BucketDTO{0, 1}, tr.Rounds[1].Matches[1].Bucket)

	_, err = svc.CloseRound(ctx, "intruder", tr.ID)
	assert.Equal(t, http.StatusForbidden, statusOf(t, err))

	// Undo with no result removes round 2.
	tr, err = svc.UndoRound(ctx, owner, tr.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, tr.CurrentRound)
	assert.False(t, tr.Rounds[0].Closed)

	tr, err = svc.CloseRound(ctx, owner, tr.ID)
	require.NoError(t, err)
	m := currentMatches(tr)[0]
	tr, err = svc.SetResult(ctx, owner, tr.ID, m.ID, ResultInput{1, 0})
	require.NoError(t, err)
	_, err = svc.UndoRound(ctx, owner, tr.ID)
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "undo with results")
}

func TestCompletion(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	// 4 players, 2 wins / 2 losses: three rounds at most.
	tr := newStarted(t, svc, 4, ConfigInput{WinsToAdvance: ptr(2), LossesToEliminate: ptr(2)})
	var err error
	for tr.Status == StatusInProgress {
		tr = winAll(t, svc, tr)
		tr, err = svc.CloseRound(ctx, owner, tr.ID)
		require.NoError(t, err)
	}
	assert.Equal(t, StatusCompleted, tr.Status)
	assert.Equal(t, 3, tr.CurrentRound)
	for _, r := range tr.Rounds {
		assert.True(t, r.Closed)
	}
	counts := map[string]int{}
	for _, p := range tr.Participants {
		counts[p.Status]++
	}
	assert.Equal(t, 2, counts[ParticipantAdvanced])
	assert.Equal(t, 2, counts[ParticipantEliminated])
	assert.Len(t, tr.Standings, 4)
	assert.Equal(t, tr.Participants[0].ID, tr.Standings[0], "seed 1 wins everything")

	_, err = svc.CloseRound(ctx, owner, tr.ID)
	assert.Equal(t, http.StatusConflict, statusOf(t, err))
	_, err = svc.SetResult(ctx, owner, tr.ID, currentMatches(tr)[0].ID, ResultInput{1, 0})
	assert.Equal(t, http.StatusConflict, statusOf(t, err))

	// Completed is read-only, undo is refused.
	_, err = svc.UndoRound(ctx, owner, tr.ID)
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "undo on completed")
}

func TestSixteenPlayersEndToEnd(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	tr := newStarted(t, svc, 16, ConfigInput{BestOf: ptr(1), DecidersBestOf: ptr(3)})
	var counts []int
	var err error
	for tr.Status == StatusInProgress {
		counts = append(counts, len(currentMatches(tr)))
		tr = winAll(t, svc, tr)
		tr, err = svc.CloseRound(ctx, owner, tr.ID)
		require.NoError(t, err)
	}
	assert.Equal(t, []int{8, 8, 8, 6, 3}, counts)
	// Round 3 deciders: a 2-0 participant plays a bo3.
	for _, m := range tr.Rounds[2].Matches {
		if m.Bucket.Wins == 2 || m.Bucket.Losses == 2 {
			assert.Equal(t, 3, m.BestOf)
		} else {
			assert.Equal(t, 1, m.BestOf)
		}
	}
	status := map[string]int{}
	for _, p := range tr.Participants {
		status[p.Status]++
	}
	assert.Equal(t, 8, status[ParticipantAdvanced])
	assert.Equal(t, 8, status[ParticipantEliminated])
}

func TestDropForfeitsCurrentMatch(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	tr := newStarted(t, svc, 4, ConfigInput{})
	m := currentMatches(tr)[0]

	_, err := svc.Drop(ctx, owner, tr.ID, "nope")
	assert.Equal(t, http.StatusNotFound, statusOf(t, err))

	tr, err = svc.Drop(ctx, owner, tr.ID, *m.P2)
	require.NoError(t, err)
	got := currentMatches(tr)[0]
	assert.True(t, got.Forfeit)
	require.NotNil(t, got.Winner)
	assert.Equal(t, m.P1, *got.Winner)
	assert.Equal(t, 1, got.P1Score)
	for _, p := range tr.Participants {
		if p.ID == *m.P2 {
			assert.Equal(t, ParticipantDropped, p.Status)
		}
	}
	_, err = svc.Drop(ctx, owner, tr.ID, *m.P2)
	assert.Equal(t, http.StatusConflict, statusOf(t, err), "drop twice")

	// The dropped participant is no longer paired.
	tr = winAll(t, svc, tr)
	tr, err = svc.CloseRound(ctx, owner, tr.ID)
	require.NoError(t, err)
	for _, mm := range currentMatches(tr) {
		assert.NotEqual(t, *m.P2, mm.P1)
		if mm.P2 != nil {
			assert.NotEqual(t, *m.P2, *mm.P2)
		}
	}
}

func TestDelete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	tr := newStarted(t, svc, 4, ConfigInput{})

	assert.Equal(t, http.StatusForbidden, statusOf(t, svc.Delete(ctx, "intruder", tr.ID)))
	require.NoError(t, svc.Delete(ctx, owner, tr.ID))
	_, err := svc.Get(ctx, tr.ID)
	assert.Equal(t, http.StatusNotFound, statusOf(t, err))

	var n int64
	require.NoError(t, svc.deps.Database(ctx).Model(&Match{}).Where("tournament_id = ?", tr.ID).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, svc.deps.Database(ctx).Model(&Participant{}).Where("tournament_id = ?", tr.ID).Count(&n).Error)
	assert.Zero(t, n)
}
