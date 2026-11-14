package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/loi"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLobbyTestServer serves the real router (no JWT) over a real game manager.
func newLobbyTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	gm, _, _ := loi.SetupTestEnvironment(t)
	s, err := NewServer(gm, nil)
	require.NoError(t, err)
	srv := httptest.NewServer(s.buildRouter(nil))
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, method, url, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, b
}

func listLobbies(t *testing.T, base string) []model.LobbySummary {
	t.Helper()
	code, body := doJSON(t, http.MethodGet, base+"/api/lois/lobbies", "")
	require.Equal(t, http.StatusOK, code)
	var out []model.LobbySummary
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

func TestLobbyRoutes(t *testing.T) {
	srv := newLobbyTestServer(t)
	api := srv.URL + "/api/lois/lobbies"

	code, body := doJSON(t, http.MethodGet, api, "")
	require.Equal(t, http.StatusOK, code)
	var raw []map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	require.Len(t, raw, 1)
	assert.ElementsMatch(t,
		[]string{"id", "name", "isDefault", "gameInProgress", "rollCount", "gameId", "players", "viewers", "createdAt"},
		keysOf(raw[0]))
	assert.Equal(t, "main", raw[0]["id"])
	assert.Equal(t, "Main", raw[0]["name"])
	assert.Equal(t, true, raw[0]["isDefault"])
	assert.Equal(t, []any{}, raw[0]["players"], "players is [] when nobody is seated, never null")
	_, err := time.Parse(time.RFC3339, raw[0]["createdAt"].(string))
	require.NoError(t, err)

	// POST without a body gets a default name.
	code, body = doJSON(t, http.MethodPost, api, "")
	require.Equal(t, http.StatusCreated, code, string(body))
	var first model.LobbySummary
	require.NoError(t, json.Unmarshal(body, &first))
	assert.Equal(t, "Lobby 2", first.Name)
	assert.Regexp(t, `^[a-z0-9]{8}$`, first.ID)
	assert.False(t, first.IsDefault)

	code, body = doJSON(t, http.MethodPost, api, `{}`)
	require.Equal(t, http.StatusCreated, code, string(body))
	code, body = doJSON(t, http.MethodPost, api, `{"name":"  Ranked night  "}`)
	require.Equal(t, http.StatusCreated, code, string(body))
	var named model.LobbySummary
	require.NoError(t, json.Unmarshal(body, &named))
	assert.Equal(t, "Ranked night", named.Name)
	code, body = doJSON(t, http.MethodPost, api, fmt.Sprintf(`{"name":%q}`, strings.Repeat("x", 60)))
	require.Equal(t, http.StatusCreated, code, string(body))
	assert.Contains(t, string(body), `"name":"`+strings.Repeat("x", 40)+`"`)

	code, body = doJSON(t, http.MethodPost, api, `{"name":`)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, string(body), `"error"`)

	list := listLobbies(t, srv.URL)
	require.Len(t, list, 5)
	assert.Equal(t, []string{"main", first.ID}, []string{list[0].ID, list[1].ID})
	assert.Equal(t, named.ID, list[3].ID)

	// Delete rules.
	code, body = doJSON(t, http.MethodDelete, api+"/main", "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, string(body), `"error"`)
	code, body = doJSON(t, http.MethodDelete, api+"/nope1234", "")
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, string(body), `"error"`)
	code, body = doJSON(t, http.MethodDelete, api+"/"+first.ID, "")
	assert.Equal(t, http.StatusNoContent, code)
	assert.Empty(t, body)
	assert.Len(t, listLobbies(t, srv.URL), 4)

	// Ten lobbies at most, main included.
	for len(listLobbies(t, srv.URL)) < 10 {
		code, body = doJSON(t, http.MethodPost, api, "")
		require.Equal(t, http.StatusCreated, code, string(body))
	}
	code, body = doJSON(t, http.MethodPost, api, `{"name":"one too many"}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, string(body), `"error"`)
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestLobbyErrorStatus(t *testing.T) {
	for err, want := range map[error]int{
		loi.ErrLobbyNotFound:                   http.StatusNotFound,
		loi.ErrLobbyLimit:                      http.StatusConflict,
		loi.ErrLobbyPermanent:                  http.StatusConflict,
		loi.ErrLobbyInProgress:                 http.StatusConflict,
		fmt.Errorf("x: %w", loi.ErrLobbyLimit): http.StatusConflict,
		errors.New("boom"):                     http.StatusInternalServerError,
	} {
		assert.Equal(t, want, lobbyErrorStatus(err), err.Error())
	}
}

func TestLobbyRoutesCORSPreflight(t *testing.T) {
	srv := newLobbyTestServer(t)
	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/api/lois/lobbies/abcd1234", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "https://tools.bonjack.club")
	req.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

func dialLobby(t *testing.T, srv *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	return c
}

func readMessage(t *testing.T, c *websocket.Conn) modelwebsocket.Message {
	t.Helper()
	var m modelwebsocket.Message
	require.NoError(t, c.ReadJSON(&m))
	return m
}

func lobbyIDOf(t *testing.T, m modelwebsocket.Message) string {
	t.Helper()
	require.Equal(t, modelwebsocket.UpdateState, m.Action)
	var gs model.GameState
	require.NoError(t, json.Unmarshal([]byte(m.Content), &gs))
	return gs.LobbyID
}

func closeCode(t *testing.T, c *websocket.Conn) int {
	t.Helper()
	_, _, err := c.ReadMessage()
	var ce *websocket.CloseError
	require.True(t, errors.As(err, &ce), "want a close frame, got %v", err)
	return ce.Code
}

func TestLobbyWebsocketRouting(t *testing.T) {
	srv := newLobbyTestServer(t)
	api := srv.URL + "/api/lois/lobbies"

	// Old clients: no lobby parameter, with or without the trailing slash.
	assert.Equal(t, "main", lobbyIDOf(t, readMessage(t, dialLobby(t, srv, "/ws"))))
	assert.Equal(t, "main", lobbyIDOf(t, readMessage(t, dialLobby(t, srv, "/ws/"))))

	unknown := dialLobby(t, srv, "/ws?lobby=nope1234")
	m := readMessage(t, unknown)
	assert.Equal(t, modelwebsocket.LobbyNotFound, m.Action)
	assert.Equal(t, "nope1234", m.Content)
	assert.Equal(t, 4404, closeCode(t, unknown))

	code, body := doJSON(t, http.MethodPost, api, `{"name":"Side"}`)
	require.Equal(t, http.StatusCreated, code)
	var side model.LobbySummary
	require.NoError(t, json.Unmarshal(body, &side))
	viewer := dialLobby(t, srv, "/ws?lobby="+side.ID)
	assert.Equal(t, side.ID, lobbyIDOf(t, readMessage(t, viewer)))
	assert.Equal(t, 1, listLobbies(t, srv.URL)[1].Viewers)

	code, _ = doJSON(t, http.MethodDelete, api+"/"+side.ID, "")
	require.Equal(t, http.StatusNoContent, code)
	m = readMessage(t, viewer)
	assert.Equal(t, modelwebsocket.LobbyClosed, m.Action)
	assert.Equal(t, side.ID, m.Content)
	assert.Equal(t, 4410, closeCode(t, viewer))
}
