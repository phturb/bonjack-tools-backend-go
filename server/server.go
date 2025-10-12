package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	jwtmiddleware "github.com/auth0/go-jwt-middleware/v2"
	"github.com/auth0/go-jwt-middleware/v2/jwks"
	"github.com/auth0/go-jwt-middleware/v2/validator"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/loi"
	modelserver "github.com/phturb/bonjack-tools-backend-go/model/server"
	modelwebsocket "github.com/phturb/bonjack-tools-backend-go/model/websocket"
)

type CustomClaims struct {
	Scope string `json:"scope"`
	Sub   string `json:"sub"`
}

func (c CustomClaims) Validate(ctx context.Context) error {
	return nil
}

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
			slog.Info(fmt.Sprintf("[ws] - closing websocket connection err : %s", err))
			break
		}
		var wm modelwebsocket.Message
		if err := json.Unmarshal(m, &wm); err != nil {
			slog.Warn(fmt.Sprintf("[ws] - unable to unmarshal the received message : %v", err))
			continue
		}
		if s.gm.HandleWebsocketMessage(&wm, conn, r) {
			slog.Info("[ws] - game manager handling the websocket")
			continue
		}
		slog.Warn("[ws] - no handlers processed the websocket message")
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
	errCh := make(chan error)
	router := mux.NewRouter()
	serverAddr := "0.0.0.0:" + internal.Config().Server.Port
	apiRouter := router.PathPrefix("/api").Subrouter()
	apiRouter.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]bool{
			"ok": true,
		}); err != nil {
			slog.Error(fmt.Sprintf("[/api/healthz] - %s", err.Error()))
			w.WriteHeader(500)
			return
		}
	})
	apiRouter.HandleFunc("/players", func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(s.gm.GetPlayers(r.Context()))
		if err != nil {
			slog.Error(fmt.Sprintf("[/api/players] - %s", err.Error()))
			w.WriteHeader(500)
			return
		}
	})
	apiRouter.HandleFunc("/games", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(s.gm.GetGames(r.Context()))
		if err != nil {
			slog.Error(fmt.Sprintf("[/api/games] - %s", err.Error()))
			w.WriteHeader(500)
			return
		}
	})
	apiRouter.HandleFunc("/rolls", func(w http.ResponseWriter, r *http.Request) {
	})

	apiRouter.HandleFunc("/players/{id}/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(s.gm.GetPlayerStats(r.Context(), mux.Vars(r)["id"]))
		if err != nil {
			slog.Error(fmt.Sprintf("[/api/players/{id}/stats] - %s", err.Error()))
			w.WriteHeader(500)
			return
		}
	})
	apiRouter.HandleFunc("/players/{id}/champions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id, ok := mux.Vars(r)["id"]
		if !ok {
			slog.Error(fmt.Sprintf("[/api/players/{id}/champions] - missing id"))
			w.WriteHeader(http.StatusBadRequest)
			return

		}
		switch r.Method {
		case http.MethodPost:
			decoder := json.NewDecoder(r.Body)
			var entry []modelserver.UpdatePlayerChampionPayload
			decoder.Decode(&entry)
			err := json.NewEncoder(w).Encode(s.gm.UpdatePlayerChampion(r.Context(), id, entry))
			if err != nil {
				slog.Error(fmt.Sprintf("[/api/players/{id}/champions] - %s", err.Error()))
				w.WriteHeader(500)
				return
			}
			return
		case http.MethodGet:
			err := json.NewEncoder(w).Encode(s.gm.GetPlayerChampion(r.Context(), id))
			if err != nil {
				slog.Error(fmt.Sprintf("[/api/players/{id}/champions] - %s", err.Error()))
				w.WriteHeader(500)
				return
			}
			return
		default:
			slog.Error(fmt.Sprintf("[/api/players/{id}/champions] - unsuported method"))
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})
	router.HandleFunc("/ws", s.handleWebsocket)
	// router.PathPrefix("/").HandlerFunc(spaHandler("static", "index.html"))
	cors := handlers.CORS(handlers.AllowedOrigins([]string{"*"}))

	if internal.Config().Server.JWTSecret != "" {
		go func() {
			issuerURL, err := url.Parse(internal.Config().Server.JWTIssuer)
			if err != nil {
				slog.Error(fmt.Sprintf("[server] - jwt issuer url failed to parse %s", err.Error()))
				errCh <- err
			}

			provider := jwks.NewCachingProvider(issuerURL, 5*time.Minute)
			jwtValidator, err := validator.New(
				provider.KeyFunc,
				validator.RS256,
				issuerURL.String(),
				internal.Config().Server.JWTAudience,
				validator.WithCustomClaims(
					func() validator.CustomClaims {
						return &CustomClaims{}
					},
				),
				validator.WithAllowedClockSkew(time.Minute),
			)
			if err != nil {
				slog.Error(fmt.Sprintf("[server] - jwt middleware failed creation %s", err.Error()))
				errCh <- err
			}
			middleware := jwtmiddleware.New(jwtValidator.ValidateToken)
			srv := &http.Server{
				Handler: cors(middleware.CheckJWT(router)),
				Addr:    serverAddr,
				// Good practice: enforce timeouts for servers you create!
				WriteTimeout: 15 * time.Second,
				ReadTimeout:  15 * time.Second,
			}

			slog.Info("[server] - starting server on port " + serverAddr)
			slog.Info("[server] - handling websocket on path : '/ws'")
			if err := srv.ListenAndServe(); err != nil {
				errCh <- err
			}
		}()
	} else {
		go func() {
			srv := &http.Server{
				Handler: cors(router),
				Addr:    serverAddr,
				// Good practice: enforce timeouts for servers you create!
				WriteTimeout: 15 * time.Second,
				ReadTimeout:  15 * time.Second,
			}

			slog.Info("[server] - starting server on port " + serverAddr)
			slog.Info("[server] - handling websocket on path : '/ws'")
			if err := srv.ListenAndServe(); err != nil {
				errCh <- err
			}
		}()
	}

	return errCh
}
