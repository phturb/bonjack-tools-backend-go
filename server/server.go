package server

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
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
	l := logger.ComponentLogger("ws")
	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		l.WithError(err).Error("failed to upgrade websocket connection")
		return
	}
	defer conn.Close()
	s.gm.HandleWebsocketConnection(conn, r)

	for {
		mt, m, err := conn.ReadMessage()
		if err != nil || mt == websocket.CloseMessage {
			l.WithError(err).Info("closing websocket connection")
			break
		}
		var wm modelwebsocket.Message
		if err := json.Unmarshal(m, &wm); err != nil {
			l.WithError(err).Warn("unable to unmarshal websocket message")
			continue
		}
		if s.gm.HandleWebsocketMessage(&wm, conn, r) {
			l.Info("game manager handling websocket message")
			continue
		}
		l.Warn("no handlers processed websocket message")
	}

	s.gm.HandleWebsocketConnection(conn, nil)
}

func spaHandler(staticPath, indexPath string) func(http.ResponseWriter, *http.Request) {
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
	l := logger.ComponentLogger("Start")
	errCh := make(chan error)
	router := mux.NewRouter()
	serverAddr := "0.0.0.0:" + config.Config().Server.Port
	apiRouter := router.PathPrefix("/api").Subrouter()
	apiRouter.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		l := logger.ComponentLogger("/api/healthz")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]bool{
			"ok": true,
		}); err != nil {
			l.WithError(err).Error("health check endpoint failed")
			w.WriteHeader(500)
			return
		}
	})
	apiRouter.HandleFunc("/players", func(w http.ResponseWriter, r *http.Request) {
		l := logger.ComponentLogger("/players")
		err := json.NewEncoder(w).Encode(s.gm.GetPlayers(r.Context()))
		if err != nil {
			l.Error("players endpoint failed", "error", err)
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
			l.Error("games endpoint failed", "error", err)
			w.WriteHeader(500)
			return
		}
	})
	apiRouter.HandleFunc("/rolls", func(w http.ResponseWriter, r *http.Request) {
	})

	apiRouter.HandleFunc("/players/{id}/stats", func(w http.ResponseWriter, r *http.Request) {
		l := logger.ComponentLogger("/players/{id}/stats")
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(s.gm.GetPlayerStats(r.Context(), mux.Vars(r)["id"]))
		if err != nil {
			l.Error("player stats endpoint failed", "error", err)
			w.WriteHeader(500)
			return
		}
	})
	apiRouter.HandleFunc("/players/{id}/champions", func(w http.ResponseWriter, r *http.Request) {
		l := logger.ComponentLogger("/players/{id}/champions")
		w.Header().Set("Content-Type", "application/json")
		id, ok := mux.Vars(r)["id"]
		if !ok {
			l.Error("player champions endpoint missing id")
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
				l.Error("player champions update endpoint failed", "error", err)
				w.WriteHeader(500)
				return
			}
			return
		case http.MethodGet:
			err := json.NewEncoder(w).Encode(s.gm.GetPlayerChampion(r.Context(), id))
			if err != nil {
				l.Error("player champions get endpoint failed", "error", err)
				w.WriteHeader(500)
				return
			}
			return
		default:
			l.Error("player champions endpoint unsupported method")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})
	router.HandleFunc("/ws", s.handleWebsocket)
	// router.PathPrefix("/").HandlerFunc(spaHandler("static", "index.html"))
	cors := handlers.CORS(handlers.AllowedOrigins([]string{"*"}))

	if config.Config().Server.JWTSecret != "" {
		go func() {
			issuerURL, err := url.Parse(config.Config().Server.JWTIssuer)
			if err != nil {
				l.Error("jwt issuer url failed to parse", "error", err)
				errCh <- err
			}

			provider := jwks.NewCachingProvider(issuerURL, 5*time.Minute)
			jwtValidator, err := validator.New(
				provider.KeyFunc,
				validator.RS256,
				issuerURL.String(),
				config.Config().Server.JWTAudience,
				validator.WithCustomClaims(
					func() validator.CustomClaims {
						return &CustomClaims{}
					},
				),
				validator.WithAllowedClockSkew(time.Minute),
			)
			if err != nil {
				l.Error("jwt middleware failed creation", "error", err)
				errCh <- err
			}
			middleware := jwtmiddleware.New(jwtValidator.ValidateToken)
			srv := &http.Server{
				Handler: cors(middleware.CheckJWT(router)),
				Addr:    serverAddr,
				// Good practice: enforce timeouts for servers you create!
				WriteTimeout: 15 * time.Second,
				ReadTimeout:  15 * time.Second,
				IdleTimeout:  60 * time.Second,
			}

			l.Info("starting server with JWT", "port", serverAddr)
			l.Info("handling websocket on path", "path", "/ws")
			if err := srv.ListenAndServe(); err != nil {
				errCh <- err
			}

			l.Info("[server] - starting server on port " + serverAddr)
			l.Info("[server] - handling websocket on path : '/ws'")
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

			l.Info("starting server without JWT", "port", serverAddr)
			l.Info("handling websocket on path", "path", "/ws")
			if err := srv.ListenAndServe(); err != nil {
				errCh <- err
			}
		}()
	}

	return errCh
}
