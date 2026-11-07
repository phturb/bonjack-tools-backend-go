// Package server provides HTTP API server and WebSocket handler functionality for the bonjack-tools-backend-go service.
// It exposes REST endpoints, manages WebSocket connections, and handles web client communication.
//
// Key features:
//   - REST API endpoints for game data and player statistics (/api/*)
//   - WebSocket connection handling for real-time updates (/ws)
//   - JWT authentication on mutating endpoints only
//   - CORS handling for cross-origin requests
//   - Static file serving for SPA support
//   - Graceful shutdown and connection management
//
// API endpoints:
//   - GET  /api/healthz: Health check endpoint
//   - GET  /api/players: All known players
//   - GET  /api/games: All games
//   - GET  /api/players/{id}/stats: Player statistics (404 when unknown)
//   - GET  /api/players/{id}/champions: Player champion pool
//   - POST /api/players/{id}/champions: Update the champion pool (JWT, owner only)
//
// WebSocket actions:
//   - Real-time player list updates
//   - Game state synchronization
//   - Champion rolling and assignment
//
// The server integrates with the GameManager for business logic and provides the
// primary interface for web clients to interact with the game system.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// maxBodyBytes bounds JSON request bodies; the champion payload is ~170 small objects.
const maxBodyBytes = 1 << 20

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
	defer s.gm.HandleWebsocketConnection(conn, nil)

	for {
		mt, m, err := conn.ReadMessage()
		if err != nil || mt == websocket.CloseMessage {
			l.WithError(err).Info("closing websocket connection")
			return
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
		return nil, errors.New("http server is not started yet")
	}
	return s.srv, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	_ = writeJSON(w, status, map[string]string{"error": msg})
}

// discordIDFromSubject extracts the Discord user id from an Auth0 subject such as
// "oauth2|discord|123456789". It returns "" for any other identity provider.
func discordIDFromSubject(sub string) string {
	parts := strings.Split(sub, "|")
	if len(parts) < 3 {
		return ""
	}
	for _, p := range parts[:len(parts)-1] {
		if p == "discord" {
			return parts[len(parts)-1]
		}
	}
	return ""
}

// subjectFromContext returns the JWT subject stored by the auth middleware.
func subjectFromContext(ctx context.Context) (string, bool) {
	claims, ok := ctx.Value(jwtmiddleware.ContextKey{}).(*validator.ValidatedClaims)
	if !ok || claims == nil {
		return "", false
	}
	return claims.RegisteredClaims.Subject, true
}

// newAuthMiddleware returns a wrapper enforcing a valid Auth0 JWT, or nil when
// JWT is not configured (local development).
func newAuthMiddleware() (func(http.Handler) http.Handler, error) {
	cfg := config.Config().Server
	if cfg.JWTSecret == "" {
		return nil, nil
	}
	issuerURL, err := url.Parse(cfg.JWTIssuer)
	if err != nil {
		return nil, fmt.Errorf("jwt issuer url failed to parse: %w", err)
	}
	provider := jwks.NewCachingProvider(issuerURL, 5*time.Minute)
	jwtValidator, err := validator.New(
		provider.KeyFunc,
		validator.RS256,
		issuerURL.String(),
		cfg.JWTAudience,
		validator.WithCustomClaims(func() validator.CustomClaims { return &CustomClaims{} }),
		validator.WithAllowedClockSkew(time.Minute),
	)
	if err != nil {
		return nil, fmt.Errorf("jwt middleware failed creation: %w", err)
	}
	return jwtmiddleware.New(jwtValidator.ValidateToken).CheckJWT, nil
}

func (s *server) buildRouter(auth func(http.Handler) http.Handler) http.Handler {
	l := logger.ComponentLogger("router")
	router := mux.NewRouter()
	apiRouter := router.PathPrefix("/api").Subrouter()

	apiRouter.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := writeJSON(w, http.StatusOK, map[string]bool{"ok": true}); err != nil {
			l.Error("health check endpoint failed", "error", err)
		}
	}).Methods(http.MethodGet)

	apiRouter.HandleFunc("/players", func(w http.ResponseWriter, r *http.Request) {
		if err := writeJSON(w, http.StatusOK, s.gm.GetPlayers(r.Context())); err != nil {
			l.Error("players endpoint failed", "error", err)
		}
	}).Methods(http.MethodGet)

	apiRouter.HandleFunc("/games", func(w http.ResponseWriter, r *http.Request) {
		if err := writeJSON(w, http.StatusOK, s.gm.GetGames(r.Context())); err != nil {
			l.Error("games endpoint failed", "error", err)
		}
	}).Methods(http.MethodGet)

	apiRouter.HandleFunc("/players/{id}/stats", func(w http.ResponseWriter, r *http.Request) {
		stats := s.gm.GetPlayerStats(r.Context(), mux.Vars(r)["id"])
		if stats.ID == "" {
			writeError(w, http.StatusNotFound, "player not found")
			return
		}
		if err := writeJSON(w, http.StatusOK, stats); err != nil {
			l.Error("player stats endpoint failed", "error", err)
		}
	}).Methods(http.MethodGet)

	apiRouter.HandleFunc("/players/{id}/champions", func(w http.ResponseWriter, r *http.Request) {
		if err := writeJSON(w, http.StatusOK, s.gm.GetPlayerChampion(r.Context(), mux.Vars(r)["id"])); err != nil {
			l.Error("player champions get endpoint failed", "error", err)
		}
	}).Methods(http.MethodGet)

	var updateChampions http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := mux.Vars(r)["id"]
		if auth != nil {
			sub, ok := subjectFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "missing token")
				return
			}
			if discordIDFromSubject(sub) != id {
				l.Warn("player champions update refused", "subject", sub, "player_id", id)
				writeError(w, http.StatusForbidden, "you can only update your own champions")
				return
			}
		}
		var entry []modelserver.UpdatePlayerChampionPayload
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&entry); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		if err := writeJSON(w, http.StatusOK, s.gm.UpdatePlayerChampion(r.Context(), id, entry)); err != nil {
			l.Error("player champions update endpoint failed", "error", err)
		}
	})
	if auth != nil {
		updateChampions = auth(updateChampions)
	}
	apiRouter.Handle("/players/{id}/champions", updateChampions).Methods(http.MethodPost)

	router.HandleFunc("/ws", s.handleWebsocket)
	// router.PathPrefix("/").HandlerFunc(spaHandler("static", "index.html"))

	cors := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{http.MethodGet, http.MethodPost, http.MethodOptions}),
		handlers.AllowedHeaders([]string{"Content-Type", "Authorization"}),
	)
	return cors(router)
}

func (s *server) Start(ctx context.Context) chan error {
	l := logger.ComponentLogger("Start")
	errCh := make(chan error, 1)

	auth, err := newAuthMiddleware()
	if err != nil {
		l.Error("failed to configure authentication", "error", err)
		errCh <- err
		return errCh
	}

	serverAddr := "0.0.0.0:" + config.Config().Server.Port
	s.srv = &http.Server{
		Handler: s.buildRouter(auth),
		Addr:    serverAddr,
		// Good practice: enforce timeouts for servers you create!
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		l.Info("starting server", "addr", serverAddr, "jwt", auth != nil)
		l.Info("handling websocket on path", "path", "/ws")
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	return errCh
}
