package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/loi"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
)

type server struct {
	srv *http.Server
	up  *websocket.Upgrader
	gm  loi.GameManager
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

func spaHandler(staticPath string, indexPath string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// Join internally call path.Clean to prevent directory traversal
		path := filepath.Join(staticPath, r.URL.Path)

		// check whether a file exists or is a directory at the given path
		fi, err := os.Stat(path)
		if os.IsNotExist(err) || fi.IsDir() {
			// file does not exist or path is a directory, serve index.html
			http.ServeFile(w, r, filepath.Join(staticPath, indexPath))
			return
		}

		if err != nil {
			// if we got an error (that wasn't that the file doesn't exist) stating the
			// file, return a 500 internal server error and stop
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// otherwise, use http.FileServer to serve the static file
		http.FileServer(http.Dir(staticPath)).ServeHTTP(w, r)
	}
}

func (s *server) GetHTTPServer() (*http.Server, error) {
	if s.srv == nil {
		return nil, errors.New("http serer is not started yet")
	}
	return s.srv, nil
}

func (s *server) Start(ctx context.Context) chan error {
	router := mux.NewRouter()
	serverAddr := "0.0.0.0:" + internal.Config().Server.Port
	slog.Info("starting server on port " + serverAddr)
	slog.Info("handling websocket on path : '/ws'")
	router.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	router.HandleFunc("/ws", s.handleWebsocket)
	router.PathPrefix("/").HandlerFunc(spaHandler("static", "index.html"))
	srv := &http.Server{
		Handler: handlers.CORS(handlers.AllowedOrigins([]string{"*"}))(router),
		Addr:    serverAddr,
		// Good practice: enforce timeouts for servers you create!
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}

	errCh := make(chan error)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()

	return errCh
}
