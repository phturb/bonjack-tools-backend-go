package modelwebsocket

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
