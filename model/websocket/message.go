// Package model provides WebSocket communication protocol definitions for the bonjack-tools-backend-go service.
// These models define the message format and actions for real-time client-server communication.
//
// Key components:
//   - Message: Standard WebSocket message structure with action and data payload
//   - Action: Enumerated message actions for game state updates and client interactions
//   - Message validation and serialization utilities
//
// Supported actions:
//   - UpdatePlayers: Real-time player list updates
//   - Roll: Champion rolling and role assignment requests
//   - Cancel: Game cancellation actions
//   - RollResult: Champion assignment results
//   - ValidateRoll: Roll validation and confirmation
//   - GetPlayerStats: Player statistics requests
//   - PlayerStatsResponse: Player statistics data
//
// Features:
//   - Type-safe action constants with validation
//   - JSON serialization for WebSocket transport
//   - Action validation and error handling
//   - Client-server communication protocol standardization
//
// These models are used by the server package for WebSocket handling and by web clients
// for real-time game state synchronization and user interactions.
package model

import "errors"

type Action string

const (
	UpdatePlayers  Action = "updatePlayers"
	Roll           Action = "roll"
	Cancel         Action = "cancel"
	Reset          Action = "reset"
	RefreshDiscord Action = "refreshDiscord"
)

var ClientActions = []Action{
	UpdatePlayers,
	Roll,
	Cancel,
	Reset,
	RefreshDiscord,
}

const (
	UpdateState Action = "updateState"
)

var ServerActions = []Action{
	UpdateState,
}

func ActionFromString(a string) (Action, error) {
	switch a {
	case string(UpdatePlayers):
		return UpdatePlayers, nil
	case string(Roll):
		return Roll, nil
	case string(Cancel):
		return Cancel, nil
	case string(Reset):
		return Reset, nil
	case string(RefreshDiscord):
		return RefreshDiscord, nil
	case string(UpdateState):
		return UpdateState, nil
	}
	return "", errors.New("unsuported action name")
}

func (s Action) String() string {
	switch s {
	case UpdatePlayers:
		return string(UpdatePlayers)
	case Roll:
		return string(Roll)
	case Cancel:
		return string(Cancel)
	case Reset:
		return string(Reset)
	case RefreshDiscord:
		return string(RefreshDiscord)
	case UpdateState:
		return string(UpdateState)
	}
	return "unknown"
}

type Message struct {
	Action  Action `json:"action"`
	Content string `json:"content,omitempty"`
}
