package loi

import (
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

func TestTickRollTimer(t *testing.T) {
	gm, _, _ := SetupTestEnvironment(t)

	gm.mu.Lock()
	gm.main.gs.CanRoll = false
	gm.main.gs.NextRollTimer = 2500
	gm.mu.Unlock()

	gm.tickRollTimer(1000)
	gm.mu.RLock()
	assert.Equal(t, uint(1500), gm.main.gs.NextRollTimer)
	assert.False(t, gm.main.gs.CanRoll)
	gm.mu.RUnlock()

	gm.tickRollTimer(1000)
	gm.tickRollTimer(1000) // 500 left: must clamp to 0, not underflow
	gm.mu.RLock()
	assert.Equal(t, uint(0), gm.main.gs.NextRollTimer)
	assert.True(t, gm.main.gs.CanRoll)
	gm.mu.RUnlock()

	gm.tickRollTimer(1000) // idle at 0 stays at 0
	gm.mu.RLock()
	assert.Equal(t, uint(0), gm.main.gs.NextRollTimer)
	gm.mu.RUnlock()
}

func TestWebsocketDisconnectRemovesOnlyThatConn(t *testing.T) {
	gm, _, _ := SetupTestEnvironment(t)
	a, b, c := &websocket.Conn{}, &websocket.Conn{}, &websocket.Conn{}

	gm.mu.Lock()
	gm.main.conns = []*websocket.Conn{a, b, c}
	for _, conn := range gm.main.conns {
		gm.connLobby[conn] = gm.main
	}
	gm.mu.Unlock()

	gm.HandleWebsocketConnection(b, nil)

	gm.mu.RLock()
	defer gm.mu.RUnlock()
	assert.Equal(t, []*websocket.Conn{a, c}, gm.main.conns)
	_, stillRegistered := gm.connLobby[b]
	assert.False(t, stillRegistered)
	assert.Len(t, gm.connLobby, 2)
}
