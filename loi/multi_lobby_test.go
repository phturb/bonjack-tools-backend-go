package loi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/loi/model"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestLobby opens a lobby and returns it.
func createTestLobby(t *testing.T, gm *gameManager, name string) *lobby {
	t.Helper()
	s, err := gm.CreateLobby(name)
	require.NoError(t, err)
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	l := gm.lobbyByID(s.ID)
	require.NotNil(t, l)
	return l
}

// plantSeats sets the seats of l directly, bypassing the seat rules, so a
// test can build states the rules would refuse (a player in two lobbies).
func plantSeats(gm *gameManager, l *lobby, ids ...string) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	players := make([]model.GamePlayer, 0, 5)
	for _, id := range ids {
		if id == "" {
			players = append(players, model.NewEmptyGamePlayer())
			continue
		}
		players = append(players, model.GamePlayer{Player: model.DiscordPlayer{ID: id, Name: stringPtr(testPlayerName(id))}})
	}
	for len(players) < 5 {
		players = append(players, model.NewEmptyGamePlayer())
	}
	l.gs.Players = players
}

func seatIDs(gm *gameManager, l *lobby) []string {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	ids := make([]string, 0, len(l.gs.Players))
	for _, p := range l.gs.Players {
		ids = append(ids, p.Player.ID)
	}
	return ids
}

func inProgress(gm *gameManager, l *lobby) bool {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	return l.gs.GameInProgress
}

// seatedIn returns availablePlayers[id].seatedIn of a payload, or nil.
func seatedIn(t *testing.T, state map[string]any, id string) map[string]any {
	t.Helper()
	ap, ok := state["availablePlayers"].(map[string]any)[id].(map[string]any)
	require.True(t, ok, "%s is not in availablePlayers", id)
	s, _ := ap["seatedIn"].(map[string]any)
	return s
}

func payloadSeats(state map[string]any) []string {
	var ids []string
	for _, p := range state["players"].([]any) {
		ids = append(ids, p.(map[string]any)["player"].(map[string]any)["id"].(string))
	}
	return ids
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestLobbiesRollIndependently(t *testing.T) {
	t.Setenv("TIMER_TIME", "300000")
	gm, mockDM, mockDeps := setupTest(t)
	timer := config.Config().GameManager.TimerTime
	ctx := context.Background()
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2", "player3", "player4"))
	b := createTestLobby(t, gm, "Second")
	plantSeats(gm, gm.main, "player1", "player2")
	plantSeats(gm, b, "player3", "player4")

	gm.roll(ctx, gm.main)
	gm.mu.RLock()
	mainGame := gm.main.gs.GameID
	assert.True(t, gm.main.gs.GameInProgress)
	assert.False(t, gm.main.gs.CanRoll)
	assert.Equal(t, timer, gm.main.gs.NextRollTimer)
	assert.False(t, b.gs.GameInProgress, "rolling main must not touch the other lobby")
	assert.True(t, b.gs.CanRoll)
	assert.Zero(t, b.gs.GameID)
	gm.mu.RUnlock()

	gm.roll(ctx, b)
	gm.mu.RLock()
	bGame := b.gs.GameID
	assert.True(t, b.gs.GameInProgress)
	assert.NotZero(t, bGame)
	assert.NotEqual(t, mainGame, bGame)
	gm.mu.RUnlock()

	// Both cooldowns run; main rolls again once it is over, b does not.
	gm.tickRollTimer(timer)
	gm.roll(ctx, gm.main)
	gm.tickRollTimer(1000)
	gm.mu.RLock()
	assert.EqualValues(t, 2, gm.main.gs.RollCount)
	assert.EqualValues(t, 1, b.gs.RollCount)
	assert.False(t, gm.main.gs.CanRoll)
	assert.Equal(t, timer-1000, gm.main.gs.NextRollTimer)
	assert.True(t, b.gs.CanRoll)
	assert.Zero(t, b.gs.NextRollTimer)
	gm.mu.RUnlock()

	rollsOf := func(gameID uint) []modeldb.GamePlayerRoll {
		var rolls []modeldb.GamePlayerRoll
		require.NoError(t, mockDeps.db.Where("game_id = ?", gameID).Order("roll_number, player_id").Find(&rolls).Error)
		return rolls
	}
	mainRolls := rollsOf(mainGame)
	require.Len(t, mainRolls, 4)
	for _, r := range mainRolls {
		assert.Contains(t, []string{"player1", "player2"}, r.PlayerID)
	}
	bRolls := rollsOf(bGame)
	require.Len(t, bRolls, 2)
	assert.Equal(t, "player3", bRolls[0].PlayerID)
	assert.Equal(t, "player4", bRolls[1].PlayerID)

	// Finishing one lobby leaves the other playing.
	gm.reset(ctx, gm.main)
	assert.False(t, inProgress(gm, gm.main))
	assert.True(t, inProgress(gm, b))
}

func TestSeatRefusedWhenPlayerIsInAGameElsewhere(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2", "player3"))
	b := createTestLobby(t, gm, "Second")
	url := newWSTestServer(t, gm)

	m := dialWS(t, url)
	m.state()
	b1 := dialWS(t, url+"?lobby="+b.id)
	require.Equal(t, b.id, b1.state()["lobbyId"])
	b2 := dialWS(t, url+"?lobby="+b.id)
	b2.state()

	m.send(modelwebsocket.UpdatePlayers, seatsContent("player1", "player2", "", "", ""))
	m.state()
	b1.state()
	b2.state()
	m.send(modelwebsocket.Roll, nil)
	m.state()
	b1.state()
	b2.state()

	// b1 picks player1, who plays in main: refused, b1 alone gets the truth.
	b1.send(modelwebsocket.UpdatePlayers, seatsContent("player3", "player1", "", "", ""))
	echo := b1.state()
	assert.Equal(t, b.id, echo["lobbyId"])
	assert.Equal(t, []string{"", "", "", "", ""}, payloadSeats(echo))
	assert.Equal(t, map[string]any{"id": "main", "name": "Main", "gameInProgress": true}, seatedIn(t, echo, "player1"))
	assert.Nil(t, seatedIn(t, echo, "player3"))
	b2.silent(200 * time.Millisecond)
	m.silent(10 * time.Millisecond)
	assert.Equal(t, []string{"", "", "", "", ""}, seatIDs(gm, b))
	assert.Equal(t, []string{"player1", "player2", "", "", ""}, seatIDs(gm, gm.main))

	// Players nobody else holds are still seated normally.
	b1.send(modelwebsocket.UpdatePlayers, seatsContent("player3", "", "", "", ""))
	assert.Equal(t, []string{"player3", "", "", "", ""}, payloadSeats(b1.state()))
	assert.Equal(t, []string{"player3", "", "", "", ""}, payloadSeats(b2.state()))
	assert.Equal(t, map[string]any{"id": b.id, "name": "Second", "gameInProgress": false}, seatedIn(t, m.state(), "player3"))
}

func TestSeatMovesPlayerOutOfAWaitingLobby(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2", "player3"))
	b := createTestLobby(t, gm, "Second")
	url := newWSTestServer(t, gm)
	m := dialWS(t, url)
	m.state()
	b1 := dialWS(t, url+"?lobby="+b.id)
	b1.state()

	m.send(modelwebsocket.UpdatePlayers, seatsContent("player1", "player2", "", "", ""))
	m.state()
	b1.state()

	// b takes player2 from main, which is only waiting: player2 moves.
	b1.send(modelwebsocket.UpdatePlayers, seatsContent("player2", "player3", "", "", ""))
	bState := b1.state()
	assert.Equal(t, []string{"player2", "player3", "", "", ""}, payloadSeats(bState))
	assert.Equal(t, map[string]any{"id": "main", "name": "Main", "gameInProgress": false}, seatedIn(t, bState, "player1"))
	assert.Nil(t, seatedIn(t, bState, "player2"))
	mState := m.state()
	assert.Equal(t, []string{"player1", "", "", "", ""}, payloadSeats(mState))
	assert.Equal(t, map[string]any{"id": b.id, "name": "Second", "gameInProgress": false}, seatedIn(t, mState, "player2"))
	assert.Equal(t, []string{"player1", "", "", "", ""}, seatIDs(gm, gm.main))

	// Main starts: the other lobby sees player1 locked.
	m.send(modelwebsocket.Roll, nil)
	m.state()
	assert.Equal(t, map[string]any{"id": "main", "name": "Main", "gameInProgress": true}, seatedIn(t, b1.state(), "player1"))
}

// Two lobbies that share a player (a state the seat rules never produce)
// start at the same moment: exactly one game starts.
func TestConcurrentStartsWithASharedPlayer(t *testing.T) {
	for i := 0; i < 10; i++ {
		gm, mockDM, mockDeps := setupTest(t)
		gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2", "player3"))
		b := createTestLobby(t, gm, "Second")
		plantSeats(gm, gm.main, "player1", "player2")
		plantSeats(gm, b, "player3", "player1")

		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, l := range []*lobby{gm.main, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				gm.roll(context.Background(), l)
			}()
		}
		close(start)
		wg.Wait()

		mainStarted, bStarted := inProgress(gm, gm.main), inProgress(gm, b)
		require.True(t, mainStarted != bStarted, "exactly one lobby must start (main %t, b %t)", mainStarted, bStarted)
		var games int64
		require.NoError(t, mockDeps.db.Model(&modeldb.Game{}).Count(&games).Error)
		require.EqualValues(t, 1, games)
	}
}

func TestVoiceLeaveUnseatsEveryWaitingLobby(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2", "player3"))
	b := createTestLobby(t, gm, "Second")
	c := createTestLobby(t, gm, "Third")
	plantSeats(gm, gm.main, "player1", "player3")
	plantSeats(gm, b, "", "player1") // planted twice to show every waiting lobby is cleared
	plantSeats(gm, c, "player2")
	gm.roll(context.Background(), c)
	require.True(t, inProgress(gm, c))

	gm.onDiscordVoiceStateUpdate(mockDM.Session(), voiceLeave("player1"))
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), voiceLeave("player2"))

	assert.Equal(t, []string{"", "player3", "", "", ""}, seatIDs(gm, gm.main))
	assert.Equal(t, []string{"", "", "", "", ""}, seatIDs(gm, b))
	assert.Equal(t, []string{"player2", "", "", "", ""}, seatIDs(gm, c), "a game in progress keeps its seats")
	gm.mu.RLock()
	assert.NotContains(t, gm.pool.players, "player1")
	assert.NotContains(t, gm.pool.players, "player2")
	gm.mu.RUnlock()
}

func TestVoiceJoinSeatsInMainOnly(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith())
	b := createTestLobby(t, gm, "Second")
	plantSeats(gm, b, "player2")

	gm.onDiscordVoiceStateUpdate(mockDM.Session(), voiceJoin("player1"))
	assert.Equal(t, []string{"player1", "", "", "", ""}, seatIDs(gm, gm.main))
	assert.Equal(t, []string{"player2", "", "", "", ""}, seatIDs(gm, b))

	// Seated in another lobby: joining voice does not seat them in main.
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), voiceJoin("player2"))
	assert.Equal(t, []string{"player1", "", "", "", ""}, seatIDs(gm, gm.main))
	assert.Equal(t, []string{"player2", "", "", "", ""}, seatIDs(gm, b))

	// Main has a game: nobody is seated anywhere.
	gm.roll(context.Background(), gm.main)
	require.True(t, inProgress(gm, gm.main))
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), voiceJoin("player3"))
	assert.Equal(t, []string{"player1", "", "", "", ""}, seatIDs(gm, gm.main))
	assert.Equal(t, []string{"player2", "", "", "", ""}, seatIDs(gm, b))
	gm.mu.RLock()
	assert.Contains(t, gm.pool.players, "player3")
	gm.mu.RUnlock()
}

func TestUnknownLobbyGetsLobbyNotFound(t *testing.T) {
	gm, _, _ := setupTest(t)
	url := newWSTestServer(t, gm)

	c := dialWS(t, url+"?lobby=nope1234")
	m := c.next()
	assert.Equal(t, modelwebsocket.LobbyNotFound, m.Action)
	assert.Equal(t, "nope1234", m.Content)
	var ce *websocket.CloseError
	require.True(t, errors.As(c.closed(), &ce))
	assert.Equal(t, 4404, ce.Code)
	gm.mu.RLock()
	assert.Empty(t, gm.connLobby)
	assert.Empty(t, gm.main.conns)
	gm.mu.RUnlock()

	// An empty lobby parameter is main, like no parameter.
	main := dialWS(t, url+"?lobby=")
	assert.Equal(t, "main", main.state()["lobbyId"])
}

func TestDeleteLobbyRules(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2"))
	assert.ErrorIs(t, gm.DeleteLobby("main"), ErrLobbyPermanent)
	assert.ErrorIs(t, gm.DeleteLobby("nope1234"), ErrLobbyNotFound)

	playing := createTestLobby(t, gm, "Playing")
	plantSeats(gm, playing, "player1")
	gm.roll(context.Background(), playing)
	require.True(t, inProgress(gm, playing))
	assert.ErrorIs(t, gm.DeleteLobby(playing.id), ErrLobbyInProgress)

	// A waiting lobby with a viewer, a seated player and a Discord message.
	doomed := createTestLobby(t, gm, "Doomed")
	plantSeats(gm, doomed, "player2")
	gm.mu.Lock()
	msgID := "doomed-message"
	doomed.messageID = &msgID
	gm.mu.Unlock()
	url := newWSTestServer(t, gm)
	viewer := dialWS(t, url+"?lobby="+doomed.id)
	viewer.state()
	m := dialWS(t, url)
	require.Equal(t, map[string]any{"id": doomed.id, "name": "Doomed", "gameInProgress": false}, seatedIn(t, m.state(), "player2"))

	require.NoError(t, gm.DeleteLobby(doomed.id))
	closed := viewer.next()
	assert.Equal(t, modelwebsocket.LobbyClosed, closed.Action)
	assert.Equal(t, doomed.id, closed.Content)
	var ce *websocket.CloseError
	require.True(t, errors.As(viewer.closed(), &ce))
	assert.Equal(t, 4410, ce.Code)
	assert.Nil(t, seatedIn(t, m.state(), "player2"), "main sees player2 free again")

	assert.ErrorIs(t, gm.DeleteLobby(doomed.id), ErrLobbyNotFound)
	gm.mu.RLock()
	assert.Nil(t, gm.lobbyByID(doomed.id))
	assert.Len(t, gm.connLobby, 1)
	gm.mu.RUnlock()
	calls := FakeDiscordAPIOf(mockDM.Session()).Calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, http.MethodPatch, last.Method)
	assert.Contains(t, last.Path, "/messages/doomed-message")
	assert.Contains(t, last.Body, "This lobby was closed.")
}

// The idle sweep removes a lobby with no game and no seated player for 30
// minutes. Watching it does not keep it open (open websockets alone must not
// hold the lobby limit forever); seat changes and game activity do.
func TestIdleLobbiesAreSwept(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	ctx := context.Background()
	clock := &fakeClock{now: time.Now()}
	gm.mu.Lock()
	gm.cfg.now = clock.Now
	gm.mu.Unlock()
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2"))
	url := newWSTestServer(t, gm)

	empty := createTestLobby(t, gm, "Empty")
	watched := createTestLobby(t, gm, "Watched")
	watcher := dialWS(t, url+"?lobby="+watched.id)
	watcher.state()
	seated := createTestLobby(t, gm, "Seated")
	plantSeats(gm, seated, "player1")
	played := createTestLobby(t, gm, "Played")
	plantSeats(gm, played, "player2")
	gm.roll(ctx, played)
	require.True(t, inProgress(gm, played))
	open := func() []string {
		var ids []string
		for _, s := range gm.ListLobbies() {
			ids = append(ids, s.ID)
		}
		return ids
	}
	expectLobbyClosed := func(c *wsClient, id string) {
		t.Helper()
		m := c.next()
		for m.Action == modelwebsocket.UpdateState { // changes in other lobbies re-send this one's state
			m = c.next()
		}
		assert.Equal(t, modelwebsocket.LobbyClosed, m.Action)
		assert.Equal(t, id, m.Content)
		var ce *websocket.CloseError
		require.True(t, errors.As(c.closed(), &ce))
		assert.Equal(t, 4410, ce.Code)
	}

	// A new viewer does not restart the idle clock.
	clock.Advance(20 * time.Minute)
	late := dialWS(t, url+"?lobby="+watched.id)
	late.state()
	clock.Advance(9 * time.Minute)
	gm.sweepIdleLobbies()
	assert.Equal(t, []string{"main", empty.id, watched.id, seated.id, played.id}, open())

	clock.Advance(2 * time.Minute)
	gm.sweepIdleLobbies()
	assert.Equal(t, []string{"main", seated.id, played.id}, open(), "a lobby with only viewers goes like an empty one")
	expectLobbyClosed(watcher, watched.id)
	expectLobbyClosed(late, watched.id)

	// 10 minutes later the seats empty in one lobby and the game ends in the
	// other, then its player leaves: both idle clocks restart from there.
	clock.Advance(10 * time.Minute)
	sc := dialWS(t, url+"?lobby="+seated.id)
	sc.state()
	sc.send(modelwebsocket.UpdatePlayers, seatsContent("", "", "", "", ""))
	sc.state()
	gm.reset(ctx, played)
	gm.onDiscordVoiceStateUpdate(mockDM.Session(), voiceLeave("player2"))
	require.Equal(t, []string{"", "", "", "", ""}, seatIDs(gm, played))

	clock.Advance(29 * time.Minute)
	gm.sweepIdleLobbies()
	assert.Equal(t, []string{"main", seated.id, played.id}, open())
	clock.Advance(2 * time.Minute)
	gm.sweepIdleLobbies()
	assert.Equal(t, []string{"main"}, open(), "main is never swept")
	expectLobbyClosed(sc, seated.id)
}

func TestTickerDrivesTimersAndSweep(t *testing.T) {
	cfg := defaultManagerConfig()
	cfg.tick = 10 * time.Millisecond
	cfg.idleTimeout = 50 * time.Millisecond
	gm, _, _ := setupTestEnvironment(t, cfg)
	gm.mu.Lock()
	gm.main.gs.CanRoll = false
	gm.main.gs.NextRollTimer = 30
	gm.mu.Unlock()
	idle := createTestLobby(t, gm, "Idle")

	require.Eventually(t, func() bool {
		gm.mu.RLock()
		defer gm.mu.RUnlock()
		return gm.main.gs.CanRoll && gm.lobbyByID(idle.id) == nil
	}, 3*time.Second, 10*time.Millisecond)
}

func TestCreateLobbyNamesAndLimit(t *testing.T) {
	gm, _, _ := setupTest(t)

	l2, err := gm.CreateLobby("")
	require.NoError(t, err)
	assert.Equal(t, "Lobby 2", l2.Name)
	l3, err := gm.CreateLobby("   ")
	require.NoError(t, err)
	assert.Equal(t, "Lobby 3", l3.Name)
	require.NoError(t, gm.DeleteLobby(l2.ID))
	again, err := gm.CreateLobby("")
	require.NoError(t, err)
	assert.Equal(t, "Lobby 2", again.Name, "the smallest free number is reused")

	named, err := gm.CreateLobby("  Ranked\n\tnight  ")
	require.NoError(t, err)
	assert.Equal(t, "Ranked night", named.Name)
	long, err := gm.CreateLobby(strings.Repeat("é", 50))
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("é", 40), long.Name)

	assert.False(t, named.IsDefault)
	assert.Regexp(t, `^[a-z0-9]{8}$`, named.ID)
	created, err := time.Parse(time.RFC3339, named.CreatedAt)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), created, time.Minute)
	assert.NotNil(t, named.Players)
	raw, err := json.Marshal(named)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"players":[]`)

	for len(gm.ListLobbies()) < 10 {
		_, err := gm.CreateLobby("")
		require.NoError(t, err)
	}
	_, err = gm.CreateLobby("one too many")
	assert.ErrorIs(t, err, ErrLobbyLimit)

	list := gm.ListLobbies()
	require.Len(t, list, 10)
	assert.Equal(t, "main", list[0].ID)
	assert.Equal(t, "Main", list[0].Name)
	assert.True(t, list[0].IsDefault)
	ids := map[string]bool{}
	for i, s := range list {
		assert.False(t, ids[s.ID], "duplicate id %s", s.ID)
		ids[s.ID] = true
		if i > 1 {
			assert.LessOrEqual(t, list[i-1].CreatedAt, s.CreatedAt, "lobbies are listed by creation")
		}
	}
}

func TestLobbySummaryShowsSeatsGameAndViewers(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2"))
	b := createTestLobby(t, gm, "Second")
	plantSeats(gm, b, "", "player2", "player1")
	gm.roll(context.Background(), b)
	url := newWSTestServer(t, gm)
	dialWS(t, url+"?lobby="+b.id).state()
	dialWS(t, url+"?lobby="+b.id).state()

	list := gm.ListLobbies()
	require.Len(t, list, 2)
	s := list[1]
	assert.Equal(t, b.id, s.ID)
	assert.True(t, s.GameInProgress)
	assert.EqualValues(t, 1, s.RollCount)
	assert.NotZero(t, s.GameID)
	assert.Equal(t, []model.LobbyPlayer{{ID: "player2", Name: stringPtr("Player 2")}, {ID: "player1", Name: stringPtr("Player 1")}}, s.Players)
	assert.Equal(t, 2, s.Viewers)
	assert.Equal(t, 0, list[0].Viewers)
}

func TestActionsFromAnUnregisteredConnectionAreDropped(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1"))
	stale := &websocket.Conn{} // e.g. a client of a lobby that was deleted
	gm.mu.RLock()
	assert.Nil(t, gm.lobbyFor(stale))
	assert.Same(t, gm.main, gm.lobbyFor(nil))
	gm.mu.RUnlock()

	content, _ := json.Marshal(seatsContent("player1"))
	gm.handleUpdatePlayers(&modelwebsocket.Message{Action: modelwebsocket.UpdatePlayers, Content: string(content)}, stale, &http.Request{})
	gm.handleRoll(&modelwebsocket.Message{Action: modelwebsocket.Roll}, stale, &http.Request{})
	assert.Equal(t, []string{"", "", "", "", ""}, seatIDs(gm, gm.main))
	assert.False(t, inProgress(gm, gm.main))
}

func ldnCommand(member *discordgo.Member) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:      "interaction-ldn",
		AppID:   "app",
		Type:    discordgo.InteractionApplicationCommand,
		GuildID: "test-guild",
		Token:   "token",
		Data:    discordgo.ApplicationCommandInteractionData{Name: "ldn"},
		Member:  member,
	}}
}

func TestDiscordMessagePerLobby(t *testing.T) {
	gm, mockDM, _ := setupTest(t)
	api := FakeDiscordAPIOf(mockDM.Session())
	gm.onGuildCreate(mockDM.Session(), guildCreateWith("player1", "player2"))
	b := createTestLobby(t, gm, "Second")
	plantSeats(gm, gm.main, "player1")
	plantSeats(gm, b, "player2")
	member := func(id string) *discordgo.Member { return &discordgo.Member{User: &discordgo.User{ID: id}} }

	// /ldn by a player seated in b posts b's message.
	gm.onInteractionCreate(mockDM.Session(), ldnCommand(member("player2")))
	gm.mu.RLock()
	require.NotNil(t, b.messageID)
	bMessage := *b.messageID
	assert.Nil(t, gm.main.messageID)
	gm.mu.RUnlock()
	calls := api.Calls()
	posted := calls[len(calls)-1].Body
	assert.Contains(t, posted, `**Second**`)
	assert.Contains(t, posted, "https://tools.bonjack.club/lois-des-norms/"+b.id)
	assert.Contains(t, posted, `"allowed_mentions":{"parse":[]`)

	// /ldn by someone seated nowhere posts main's.
	gm.onInteractionCreate(mockDM.Session(), ldnCommand(member("player9")))
	gm.mu.RLock()
	require.NotNil(t, gm.main.messageID)
	mainMessage := *gm.main.messageID
	gm.mu.RUnlock()
	assert.NotEqual(t, bMessage, mainMessage)
	calls = api.Calls()
	assert.Contains(t, calls[len(calls)-1].Body, "https://tools.bonjack.club/lois-des-norms/main")

	// A click resolves its lobby by message id, and only that lobby's players count.
	gm.onInteractionCreate(mockDM.Session(), buttonClick(bMessage, "roll_btn", member("player1"), nil))
	assert.False(t, inProgress(gm, b), "player1 sits in main, not in b")
	gm.onInteractionCreate(mockDM.Session(), buttonClick("unknown-message", "roll_btn", member("player2"), nil))
	assert.False(t, inProgress(gm, b))
	gm.onInteractionCreate(mockDM.Session(), buttonClick(bMessage, "roll_btn", member("player2"), nil))
	assert.True(t, inProgress(gm, b))
	assert.False(t, inProgress(gm, gm.main))
	calls = api.Calls()
	assert.Contains(t, calls[len(calls)-1].Path, "/messages/"+bMessage, "the roll edits b's message")

	gm.onInteractionCreate(mockDM.Session(), buttonClick(mainMessage, "roll_btn", member("player1"), nil))
	assert.True(t, inProgress(gm, gm.main))
	gm.onInteractionCreate(mockDM.Session(), buttonClick(bMessage, "cancel_btn", member("player2"), nil))
	assert.False(t, inProgress(gm, b))
	assert.True(t, inProgress(gm, gm.main))
}

func TestDiscordMessageEscapesTheLobbyName(t *testing.T) {
	l := newLobby("abcd1234", "*bold* _x_ `y` <@&1>", time.Now())
	text, _, err := createDiscordMessage(l, l.gs)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(text, "**\\*bold\\* \\_x\\_ \\`y\\` <@&1\\>**\n"), text)
}
