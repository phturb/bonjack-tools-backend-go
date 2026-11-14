package loi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"github.com/stretchr/testify/require"
)

// newWSTestServer serves gm the way server.handleWebsocket does: connect,
// read loop, disconnect. The server package imports loi, so loi tests can't
// use it directly. It returns the ws:// URL of the /ws endpoint.
func newWSTestServer(t *testing.T, gm GameManager) string {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		gm.HandleWebsocketConnection(conn, r)
		defer gm.HandleWebsocketConnection(conn, nil)
		for {
			mt, m, err := conn.ReadMessage()
			if err != nil || mt == websocket.CloseMessage {
				return
			}
			var wm modelwebsocket.Message
			if err := json.Unmarshal(m, &wm); err != nil {
				continue
			}
			gm.HandleWebsocketMessage(&wm, conn, r)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

// wsClient is a test websocket client. A background reader queues every
// frame, so a test can wait for the next one or assert that none arrives.
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
	msgs chan modelwebsocket.Message
	done chan struct{}
	err  error // the error that stopped the reader; read it only after done is closed
}

func dialWS(t *testing.T, url string) *wsClient {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	c := &wsClient{t: t, conn: conn, msgs: make(chan modelwebsocket.Message, 64), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			var m modelwebsocket.Message
			if err := conn.ReadJSON(&m); err != nil {
				c.err = err
				return
			}
			c.msgs <- m
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-c.done
	})
	return c
}

func (c *wsClient) send(action modelwebsocket.Action, content any) {
	c.t.Helper()
	m := modelwebsocket.Message{Action: action}
	if content != nil {
		b, err := json.Marshal(content)
		require.NoError(c.t, err)
		m.Content = string(b)
	}
	require.NoError(c.t, c.conn.WriteJSON(m))
}

func (c *wsClient) next() modelwebsocket.Message {
	c.t.Helper()
	select {
	case m := <-c.msgs:
		return m
	case <-time.After(3 * time.Second):
		c.t.Fatal("no websocket frame within 3s")
	}
	return modelwebsocket.Message{}
}

// state reads the next frame, which must be an updateState, and returns its
// content as a generic JSON object.
func (c *wsClient) state() map[string]any {
	c.t.Helper()
	m := c.next()
	require.Equal(c.t, modelwebsocket.UpdateState, m.Action)
	var raw map[string]any
	require.NoError(c.t, json.Unmarshal([]byte(m.Content), &raw))
	return raw
}

// silent asserts that no frame arrives within d.
func (c *wsClient) silent(d time.Duration) {
	c.t.Helper()
	select {
	case m := <-c.msgs:
		c.t.Fatalf("unexpected frame %s %s", m.Action, m.Content)
	case <-time.After(d):
	}
}

// closed waits for the server to close the connection and returns the error
// the reader stopped on (a *websocket.CloseError for a clean close).
func (c *wsClient) closed() error {
	c.t.Helper()
	select {
	case <-c.done:
		return c.err
	case <-time.After(3 * time.Second):
		c.t.Fatal("connection still open after 3s")
	}
	return nil
}

func seatsContent(ids ...string) []map[string]string {
	out := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]string{"id": id})
	}
	return out
}

// testPlayerName is the voice display name the tests give a seeded player:
// "player3" is "Player 3".
func testPlayerName(id string) string {
	return "Player " + strings.TrimPrefix(id, "player")
}

func guildCreateWith(ids ...string) *discordgo.GuildCreate {
	gc := &discordgo.GuildCreate{Guild: &discordgo.Guild{
		ID:       "test-guild",
		Name:     "Test Guild",
		Channels: []*discordgo.Channel{{ID: "test-channel", Name: "Test Channel"}},
	}}
	for _, id := range ids {
		gc.VoiceStates = append(gc.VoiceStates, &discordgo.VoiceState{UserID: id, ChannelID: "test-channel"})
		gc.Members = append(gc.Members, &discordgo.Member{User: &discordgo.User{ID: id}, Nick: testPlayerName(id)})
	}
	return gc
}

func voiceJoin(id string) *discordgo.VoiceStateUpdate {
	return &discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		UserID: id, ChannelID: "test-channel",
		Member: &discordgo.Member{User: &discordgo.User{ID: id}, Nick: testPlayerName(id)},
	}}
}

func voiceLeave(id string) *discordgo.VoiceStateUpdate {
	return &discordgo.VoiceStateUpdate{
		VoiceState:   &discordgo.VoiceState{UserID: id, ChannelID: ""},
		BeforeUpdate: &discordgo.VoiceState{UserID: id, ChannelID: "test-channel"},
	}
}
