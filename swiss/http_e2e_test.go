package swiss

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

// e2eClient drives the real handlers through the real router, the way the
// website does, and asserts on the JSON contract rather than on Go types.
type e2eClient struct {
	t   *testing.T
	srv *httptest.Server
}

func newE2E(t *testing.T) *e2eClient {
	t.Helper()
	r := mux.NewRouter()
	RegisterRoutes(r.PathPrefix("/api").Subrouter(), nil, nil, newTestService(t))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &e2eClient{t: t, srv: srv}
}

func (c *e2eClient) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	return c.doAs("", method, path, body)
}

// doAs sends the request with `Authorization: Bearer <subject>` when subject is
// non-empty; in dev mode (no JWT) the backend uses it as the caller identity.
func (c *e2eClient) doAs(subject, method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(c.t, json.NewEncoder(&buf).Encode(body))
	}
	req, err := http.NewRequest(method, c.srv.URL+"/api"+path, &buf)
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", "application/json")
	if subject != "" {
		req.Header.Set("Authorization", "Bearer "+subject)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer res.Body.Close()
	var out map[string]any
	if res.StatusCode != http.StatusNoContent {
		require.NoError(c.t, json.NewDecoder(res.Body).Decode(&out), "%s %s", method, path)
	}
	return res.StatusCode, out
}

func (c *e2eClient) list() (int, []map[string]any) {
	c.t.Helper()
	res, err := http.Get(c.srv.URL + "/api/swiss")
	require.NoError(c.t, err)
	defer res.Body.Close()
	var out []map[string]any
	require.NoError(c.t, json.NewDecoder(res.Body).Decode(&out))
	return res.StatusCode, out
}

func matchesOfRound(t map[string]any, n int) []map[string]any {
	rounds := t["rounds"].([]any)
	round := rounds[n-1].(map[string]any)
	raw := round["matches"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestHTTPEndToEnd(t *testing.T) {
	c := newE2E(t)

	// Create with defaults, check the contract the website relies on.
	code, tr := c.do(http.MethodPost, "/swiss", map[string]any{"name": "Friday Swiss"})
	require.Equal(t, http.StatusCreated, code)
	id := tr["id"].(string)
	require.Len(t, id, 10)
	require.Equal(t, "draft", tr["status"])
	require.Equal(t, "dev", tr["ownerSub"])
	cfg := tr["config"].(map[string]any)
	require.EqualValues(t, 3, cfg["winsToAdvance"])
	require.EqualValues(t, 3, cfg["lossesToEliminate"])
	require.EqualValues(t, 1, cfg["bestOf"])
	require.EqualValues(t, 1, cfg["decidersBestOf"], "decidersBestOf defaults to bestOf")
	require.Equal(t, "public", cfg["visibility"])
	require.NotNil(t, tr["participants"])
	require.NotNil(t, tr["rounds"])
	require.NotNil(t, tr["standings"])

	// Patch config and reject bad values.
	code, tr = c.do(http.MethodPatch, "/swiss/"+id, map[string]any{"bestOf": 3, "decidersBestOf": 5, "seeding": "manual"})
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 3, tr["config"].(map[string]any)["bestOf"])
	code, _ = c.do(http.MethodPatch, "/swiss/"+id, map[string]any{"bestOf": 4})
	require.Equal(t, http.StatusUnprocessableEntity, code)

	// Participants: pasted list, seeds renumbered.
	names := []string{"Alice", "Bob", "Carol", "Dave", "Eve"}
	ps := make([]map[string]any, 0, len(names))
	for i, n := range names {
		ps = append(ps, map[string]any{"name": n, "seed": i + 1})
	}
	code, tr = c.do(http.MethodPut, "/swiss/"+id+"/participants", ps)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, tr["participants"], 5)

	code, _ = c.do(http.MethodPut, "/swiss/"+id+"/participants", []map[string]any{{"name": "x"}, {"name": " X "}})
	require.Equal(t, http.StatusUnprocessableEntity, code, "duplicate names")

	// Summary list.
	code, list := c.list()
	require.Equal(t, http.StatusOK, code)
	require.Len(t, list, 1)
	require.Equal(t, id, list[0]["id"])
	require.EqualValues(t, 5, list[0]["participants"])
	require.Contains(t, list[0], "createdAt")

	// Start: round 1 with 5 players = 2 matches + 1 bye.
	code, tr = c.do(http.MethodPost, "/swiss/"+id+"/start", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "in_progress", tr["status"])
	require.EqualValues(t, 1, tr["currentRound"])
	r1 := matchesOfRound(tr, 1)
	require.Len(t, r1, 3)
	byes := 0
	var open []map[string]any
	for _, m := range r1 {
		if m["bye"].(bool) {
			byes++
			require.Nil(t, m["p2"])
			require.NotNil(t, m["winner"])
		} else {
			require.Nil(t, m["winner"])
			require.EqualValues(t, 0, m["p1Score"])
			open = append(open, m)
		}
	}
	require.Equal(t, 1, byes)
	require.Len(t, open, 2)

	// Config frozen after start, name still editable.
	code, _ = c.do(http.MethodPatch, "/swiss/"+id, map[string]any{"winsToAdvance": 2})
	require.Equal(t, http.StatusConflict, code)
	code, tr = c.do(http.MethodPatch, "/swiss/"+id, map[string]any{"name": "Renamed"})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "Renamed", tr["name"])

	// Close with unresolved matches is refused; results validated against bo3.
	code, _ = c.do(http.MethodPost, "/swiss/"+id+"/rounds/close", nil)
	require.Equal(t, http.StatusConflict, code)
	mid := open[0]["id"].(string)
	code, _ = c.do(http.MethodPut, "/swiss/"+id+"/matches/"+mid+"/result", map[string]any{"p1Score": 1, "p2Score": 0})
	require.Equal(t, http.StatusUnprocessableEntity, code, "1-0 is not a bo3 result")
	code, tr = c.do(http.MethodPut, "/swiss/"+id+"/matches/"+mid+"/result", map[string]any{"p1Score": 2, "p2Score": 1})
	require.Equal(t, http.StatusOK, code)
	code, _ = c.do(http.MethodPost, "/swiss/"+id+"/rounds/undo", nil)
	require.Equal(t, http.StatusConflict, code, "undo with a result and on round 1")

	// Play every round, higher seed wins, until completed.
	rounds := 1
	for tr["status"] == "in_progress" {
		for _, m := range matchesOfRound(tr, rounds) {
			if m["winner"] != nil {
				continue
			}
			bo := int(m["bestOf"].(float64))
			win := bo/2 + 1
			code, tr = c.do(http.MethodPut, "/swiss/"+id+"/matches/"+m["id"].(string)+"/result", map[string]any{"p1Score": win, "p2Score": 0})
			require.Equal(t, http.StatusOK, code)
		}
		code, tr = c.do(http.MethodPost, "/swiss/"+id+"/rounds/close", nil)
		require.Equal(t, http.StatusOK, code)
		rounds++
		require.LessOrEqual(t, rounds, 6, "runaway tournament")
	}
	require.Equal(t, "completed", tr["status"])
	statuses := map[string]int{}
	for _, p := range tr["participants"].([]any) {
		statuses[p.(map[string]any)["status"].(string)]++
	}
	require.Equal(t, 0, statuses["active"])
	require.Equal(t, 5, statuses["advanced"]+statuses["eliminated"])
	require.Len(t, tr["standings"], 5)

	code, _ = c.do(http.MethodPost, "/swiss/"+id+"/rounds/undo", nil)
	require.Equal(t, http.StatusConflict, code, "undo on completed")

	// Delete and 404 afterwards.
	code, _ = c.do(http.MethodDelete, "/swiss/"+id, nil)
	require.Equal(t, http.StatusNoContent, code)
	code, _ = c.do(http.MethodGet, "/swiss/"+id, nil)
	require.Equal(t, http.StatusNotFound, code)
}

func TestHTTPUndoGeneratedRound(t *testing.T) {
	c := newE2E(t)
	_, tr := c.do(http.MethodPost, "/swiss", map[string]any{"name": "Undo"})
	id := tr["id"].(string)
	_, _ = c.do(http.MethodPut, "/swiss/"+id+"/participants", []map[string]any{{"name": "A"}, {"name": "B"}, {"name": "C"}, {"name": "D"}})
	_, tr = c.do(http.MethodPost, "/swiss/"+id+"/start", nil)
	for _, m := range matchesOfRound(tr, 1) {
		_, tr = c.do(http.MethodPut, "/swiss/"+id+"/matches/"+m["id"].(string)+"/result", map[string]any{"p1Score": 1, "p2Score": 0})
	}
	code, tr := c.do(http.MethodPost, "/swiss/"+id+"/rounds/close", nil)
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 2, tr["currentRound"])
	code, tr = c.do(http.MethodPost, "/swiss/"+id+"/rounds/undo", nil)
	require.Equal(t, http.StatusOK, code, "round 2 has no results, undo allowed")
	require.EqualValues(t, 1, tr["currentRound"])
	require.Len(t, tr["rounds"], 1)
	require.Equal(t, false, tr["rounds"].([]any)[0].(map[string]any)["closed"], "round 1 reopened")
}

func TestHTTPDevIdentityBypass(t *testing.T) {
	c := newE2E(t)

	// No token: owner is the literal "dev".
	code, tr := c.do(http.MethodPost, "/swiss", map[string]any{"name": "Default owner"})
	require.Equal(t, http.StatusCreated, code)
	require.Equal(t, "dev", tr["ownerSub"])

	// Bearer value becomes the owner in dev mode.
	code, tr = c.doAs("alice", http.MethodPost, "/swiss", map[string]any{"name": "Alice's"})
	require.Equal(t, http.StatusCreated, code)
	require.Equal(t, "alice", tr["ownerSub"])
	id := tr["id"].(string)

	// Another identity is a non-owner: 403. Owner and "dev" default do not mix either.
	code, _ = c.doAs("bob", http.MethodPatch, "/swiss/"+id, map[string]any{"name": "Bob's"})
	require.Equal(t, http.StatusForbidden, code)
	code, _ = c.do(http.MethodPatch, "/swiss/"+id, map[string]any{"name": "Dev's"})
	require.Equal(t, http.StatusForbidden, code)
	code, tr = c.doAs("alice", http.MethodPatch, "/swiss/"+id, map[string]any{"name": "Still Alice's"})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "Still Alice's", tr["name"])

	// Header present but empty value falls back to "dev".
	code, tr = c.doAs(" ", http.MethodPost, "/swiss", map[string]any{"name": "Blank"})
	require.Equal(t, http.StatusCreated, code)
	require.Equal(t, "dev", tr["ownerSub"])
}
