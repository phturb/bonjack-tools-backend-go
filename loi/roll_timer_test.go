package loi

import (
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

func TestTickRollTimer(t *testing.T) {
	gm, _, _ := SetupTestEnvironment(t)

	gm.gsMu.Lock()
	gm.gs.CanRoll = false
	gm.gs.NextRollTimer = 2500
	gm.gsMu.Unlock()

	gm.tickRollTimer(1000)
	gm.gsMu.RLock()
	assert.Equal(t, uint(1500), gm.gs.NextRollTimer)
	assert.False(t, gm.gs.CanRoll)
	gm.gsMu.RUnlock()

	gm.tickRollTimer(1000)
	gm.tickRollTimer(1000) // 500 left: must clamp to 0, not underflow
	gm.gsMu.RLock()
	assert.Equal(t, uint(0), gm.gs.NextRollTimer)
	assert.True(t, gm.gs.CanRoll)
	gm.gsMu.RUnlock()

	gm.tickRollTimer(1000) // idle at 0 stays at 0
	gm.gsMu.RLock()
	assert.Equal(t, uint(0), gm.gs.NextRollTimer)
	gm.gsMu.RUnlock()
}

func TestWebsocketDisconnectRemovesOnlyThatConn(t *testing.T) {
	gm, _, _ := SetupTestEnvironment(t)
	a, b, c := &websocket.Conn{}, &websocket.Conn{}, &websocket.Conn{}

	gm.connsMu.Lock()
	gm.conns = []*websocket.Conn{a, b, c}
	gm.connsMu.Unlock()

	gm.HandleWebsocketConnection(b, nil)

	gm.connsMu.RLock()
	defer gm.connsMu.RUnlock()
	assert.Equal(t, []*websocket.Conn{a, c}, gm.conns)
}
