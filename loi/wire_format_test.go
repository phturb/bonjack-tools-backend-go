package loi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wireState describes one expected updateState payload.
type wireState struct {
	seats      []string // player id per seat, "" for an empty seat
	rolled     bool     // every seat carries a role, seated players a champion too
	rollCount  int
	inProgress bool
	avail      []string // player ids in the voice channel
	gameID     int
	timer      uint
	canRoll    bool
	version    string
}

func (w wireState) json() string {
	seats := make([]string, 0, len(w.seats))
	for _, id := range w.seats {
		if id == "" {
			role := "null"
			if w.rolled {
				role = `"ROLE"` // a roll gives empty seats a role too, without a champion
			}
			seats = append(seats, fmt.Sprintf(`{"player":{"id":"","name":null},"role":%s,"champion":null}`, role))
			continue
		}
		rc := `"role":null,"champion":null`
		if w.rolled {
			rc = `"role":"ROLE","champion":"CHAMPION"`
		}
		seats = append(seats, fmt.Sprintf(`{"player":{"id":%q,"name":%q},%s}`, id, testPlayerName(id), rc))
	}
	avail := make([]string, 0, len(w.avail))
	for _, id := range w.avail {
		avail = append(avail, fmt.Sprintf(`%q:{"id":%q,"name":%q,"stats":{"maxNumberOfRoll":0,"numberOfGame":0,"totalRoll":0}}`, id, id, testPlayerName(id)))
	}
	return fmt.Sprintf(`{
		"players":[%s],
		"rollCount":%d,
		"gameInProgress":%t,
		"availablePlayers":{%s},
		"gameId":%d,
		"nextRollTimer":%d,
		"canRoll":%t,
		"discordGuild":"test-guild",
		"discordGuildName":"Test Guild",
		"discordGuildChannelId":"test-channel",
		"discordGuildChannel":"Test Channel",
		"leagueVersion":%q
	}`, strings.Join(seats, ","), w.rollCount, w.inProgress, strings.Join(avail, ","), w.gameID, w.timer, w.canRoll, w.version)
}

// normalizeRolls checks every rolled seat and replaces its random role and
// champion with placeholders, so the payload can be compared exactly.
func normalizeRolls(t *testing.T, raw map[string]any) {
	t.Helper()
	roles := map[string]bool{"ADC": true, "JUNGLE": true, "SUPPORT": true, "TOP": true, "MID": true}
	seen := map[string]bool{}
	for _, p := range raw["players"].([]any) {
		seat := p.(map[string]any)
		if seat["role"] != nil {
			require.True(t, roles[seat["role"].(string)], "unknown role %v", seat["role"])
			seat["role"] = "ROLE"
		}
		if seat["champion"] != nil {
			c := seat["champion"].(map[string]any)
			require.NotEmpty(t, c["id"])
			require.NotEmpty(t, c["name"])
			require.NotEmpty(t, c["img"])
			require.False(t, seen[c["id"].(string)], "champion %v rolled twice", c["id"])
			seen[c["id"].(string)] = true
			seat["champion"] = "CHAMPION"
		}
	}
}

func assertWire(t *testing.T, want wireState, raw map[string]any) {
	t.Helper()
	normalizeRolls(t, raw)
	got, err := json.Marshal(raw)
	require.NoError(t, err)
	assert.JSONEq(t, want.json(), string(got))
}

// TestWireFormatSingleLobby pins what today's website receives on /ws: the
// exact updateState keys and values, and which client gets each frame.
// Multi-lobby support must keep this unchanged for the main lobby.
func TestWireFormatSingleLobby(t *testing.T) {
	t.Setenv("TIMER_TIME", "300000")
	gm, mockDM, mockDeps := setupTest(t)
	timer := config.Config().GameManager.TimerTime
	require.EqualValues(t, 300000, timer)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2"))
	url := newWSTestServer(t, gm)

	empty := []string{"", "", "", "", ""}
	idle := wireState{seats: empty, avail: []string{"player1", "player2"}, canRoll: true}

	// Connecting sends the current state to the new client only.
	a := dialWS(t, url)
	assertWire(t, idle, a.state())
	b := dialWS(t, url)
	assertWire(t, idle, b.state())
	a.silent(100 * time.Millisecond)

	// Seat change: broadcast to every client, the sender included.
	a.send(modelwebsocket.UpdatePlayers, seatsContent("player1", "player2", "", "", ""))
	seated := idle
	seated.seats = []string{"player1", "player2", "", "", ""}
	assertWire(t, seated, a.state())
	assertWire(t, seated, b.state())

	// First roll starts game 1.
	a.send(modelwebsocket.Roll, nil)
	rolled := seated
	rolled.rolled, rolled.rollCount, rolled.inProgress, rolled.gameID = true, 1, true, 1
	rolled.timer, rolled.canRoll, rolled.version = timer, false, "14.1.1"
	assertWire(t, rolled, a.state())
	assertWire(t, rolled, b.state())

	// Seat change during a game: refused, the true state goes to the sender only.
	b.send(modelwebsocket.UpdatePlayers, seatsContent("player2", "player1", "", "", ""))
	echo := b.state()
	require.LessOrEqual(t, echo["nextRollTimer"], float64(timer))
	echo["nextRollTimer"] = float64(timer) // the roll cooldown may have ticked
	assertWire(t, rolled, echo)
	a.silent(200 * time.Millisecond)

	// Roll during the cooldown: refused silently.
	a.send(modelwebsocket.Roll, nil)
	a.silent(200 * time.Millisecond)
	b.silent(10 * time.Millisecond)

	// Finish: seats stay, roles and champions go, the game id stays.
	a.send(modelwebsocket.Reset, nil)
	finished := seated
	finished.gameID, finished.version = 1, "14.1.1"
	assertWire(t, finished, a.state())
	assertWire(t, finished, b.state())

	// Voice leave empties the seat; voice join takes the first empty seat.
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), voiceLeave("player2"))
	left := finished
	left.seats = []string{"player1", "", "", "", ""}
	left.avail = []string{"player1"}
	assertWire(t, left, a.state())
	assertWire(t, left, b.state())
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), voiceJoin("player3"))
	joined := left
	joined.seats = []string{"player1", "player3", "", "", ""}
	joined.avail = []string{"player1", "player3"}
	assertWire(t, joined, a.state())
	assertWire(t, joined, b.state())

	// Cancel a running game: game 2 is deleted and the game id goes back to 0.
	b.send(modelwebsocket.Roll, nil)
	running := joined
	running.rolled, running.rollCount, running.inProgress, running.gameID = true, 1, true, 2
	running.timer, running.canRoll = timer, false
	assertWire(t, running, a.state())
	assertWire(t, running, b.state())
	b.send(modelwebsocket.Cancel, nil)
	cancelled := joined
	cancelled.gameID = 0
	assertWire(t, cancelled, a.state())
	assertWire(t, cancelled, b.state())
	var games []modeldb.Game
	require.NoError(t, mockDeps.db.Order("id").Find(&games).Error)
	require.Len(t, games, 1)
	require.EqualValues(t, 1, games[0].ID)
	require.NotNil(t, games[0].FinishedAt)

	a.silent(100 * time.Millisecond)
	b.silent(10 * time.Millisecond)
}
