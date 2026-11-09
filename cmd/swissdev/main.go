// Command swissdev runs only the Swiss tournament API on a local SQLite file,
// without Discord, Postgres or JWT. It exists for frontend development and
// manual end-to-end checks:
//
//	go run ./cmd/swissdev            # listens on :3001, data in swissdev.db
//	SWISSDEV_ADDR=:4000 SWISSDEV_DB=/tmp/x.db go run ./cmd/swissdev
//
// Every mutation is attributed to the subject sent as `Authorization: Bearer
// <subject>` (any plain string, no JWT), or to "dev" when the header is absent.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/phturb/bonjack-tools-backend-go/swiss"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type deps struct{ db *gorm.DB }

func (d *deps) Database(ctx context.Context) *gorm.DB { return d.db.WithContext(ctx) }
func (d *deps) Cron(context.Context) *cron.Cron       { return cron.New() }

func main() {
	addr := os.Getenv("SWISSDEV_ADDR")
	if addr == "" {
		addr = ":3001"
	}
	path := os.Getenv("SWISSDEV_DB")
	if path == "" {
		path = "swissdev.db"
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	sqlDB, err := db.DB()
	if err != nil {
		slog.Error("database handle", "error", err)
		os.Exit(1)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(swiss.Models()...); err != nil {
		slog.Error("migrate", "error", err)
		os.Exit(1)
	}

	router := mux.NewRouter()
	api := router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	swiss.RegisterRoutes(api, nil, nil, swiss.NewService(&deps{db: db}))

	cors := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}),
		handlers.AllowedHeaders([]string{"Content-Type", "Authorization"}),
	)
	srv := &http.Server{Addr: addr, Handler: cors(router), ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
	slog.Info("swissdev listening", "addr", addr, "db", path)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server", "error", err)
		os.Exit(1)
	}
}
