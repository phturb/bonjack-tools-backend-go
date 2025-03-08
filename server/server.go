package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/loi"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
)

type server struct {
	up *websocket.Upgrader
	gm loi.GameManager
}

func NewServer(gm loi.GameManager) (*server, error) {
	return &server{
		up: &websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Accepting all requests
			},
		},
		gm: gm,
	}, nil
}

func (s *server) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		slog.Error(err.Error())
		return
	}
	defer conn.Close()
	s.gm.HandleWebsocketConnection(conn, r)

	for {
		mt, m, err := conn.ReadMessage()
		if err != nil || mt == websocket.CloseMessage {
			slog.Info(fmt.Sprintf("closing websocket connection err : %s", err))
			break
		}
		var wm modelwebsocket.Message
		if err := json.Unmarshal(m, &wm); err != nil {
			slog.Warn(fmt.Sprintf("unable to unmarshal the received message : %v", err))
			continue
		}
		if s.gm.HandleWebsocketMessage(&wm, conn, r) {
			slog.Info("game manager handling the websocket")
			continue
		}
		slog.Warn("no handlers processed the websocket message")
	}
}

func (s *server) Start(ctx context.Context) error {
	slog.Info("starting server on port :" + internal.Config().Server.Port)
	http.HandleFunc("/", s.handleWebsocket)
	return http.ListenAndServe(":"+internal.Config().Server.Port, nil)
}
