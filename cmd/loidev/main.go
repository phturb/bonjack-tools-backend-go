// Command loidev runs the Lois des Norms backend for website development,
// without Discord or Postgres. It serves the real loi game manager behind the
// real server router (/ws, /api/lois/*, /api/players/*, /api/swiss/*) on
// SQLite seeded with champions, a League version and the lane roles. The
// Discord session is never opened and its REST calls fail at once, and the
// voice channel is fake:
//
//	go run ./cmd/loidev        # listens on :3001, fresh in-memory database
//	LOIDEV_ADDR=:4000 LOIDEV_DB=loidev.db LOIDEV_PLAYERS=alice,bob,carol go run ./cmd/loidev
//
// Every id in LOIDEV_PLAYERS (default alice,bob,carol,dave,erin,frank,grace,
// heidi,ivan,judy; display name = id capitalized) is in the voice channel at
// start. Simulate voice events with:
//
//	curl -X POST localhost:3001/api/dev/voice/alice/leave
//	curl -X POST localhost:3001/api/dev/voice/alice/join
//	curl -X POST 'localhost:3001/api/dev/voice/zoe/join?name=Zoe'
//
// TIMER_TIME (the roll cooldown, in ms) defaults to 10000 here. JWT is off.
// LOIDEV_DB keeps the games in a file, so a restart restores the unfinished
// ones like a deploy would.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
	"github.com/phturb/bonjack-tools-backend-go/loi"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"github.com/phturb/bonjack-tools-backend-go/server"
	"github.com/phturb/bonjack-tools-backend-go/swiss"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

const (
	devGuildID   = "loidev-guild"
	devChannelID = "loidev-voice"
	// leagueVersion is a Data Dragon version, so champion icons load.
	leagueVersion = "14.24.1"
)

var defaultPlayers = []string{"alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi", "ivan", "judy"}

// champions is a slice of Data Dragon: key, display name, image file.
var champions = []modeldb.Champion{
	{ID: "266", Name: "Aatrox", Img: "Aatrox.png"},
	{ID: "103", Name: "Ahri", Img: "Ahri.png"},
	{ID: "84", Name: "Akali", Img: "Akali.png"},
	{ID: "32", Name: "Amumu", Img: "Amumu.png"},
	{ID: "1", Name: "Annie", Img: "Annie.png"},
	{ID: "22", Name: "Ashe", Img: "Ashe.png"},
	{ID: "53", Name: "Blitzcrank", Img: "Blitzcrank.png"},
	{ID: "51", Name: "Caitlyn", Img: "Caitlyn.png"},
	{ID: "122", Name: "Darius", Img: "Darius.png"},
	{ID: "81", Name: "Ezreal", Img: "Ezreal.png"},
	{ID: "86", Name: "Garen", Img: "Garen.png"},
	{ID: "222", Name: "Jinx", Img: "Jinx.png"},
	{ID: "64", Name: "Lee Sin", Img: "LeeSin.png"},
	{ID: "89", Name: "Leona", Img: "Leona.png"},
	{ID: "99", Name: "Lux", Img: "Lux.png"},
	{ID: "54", Name: "Malphite", Img: "Malphite.png"},
	{ID: "11", Name: "Master Yi", Img: "MasterYi.png"},
	{ID: "21", Name: "Miss Fortune", Img: "MissFortune.png"},
	{ID: "25", Name: "Morgana", Img: "Morgana.png"},
	{ID: "75", Name: "Nasus", Img: "Nasus.png"},
	{ID: "17", Name: "Teemo", Img: "Teemo.png"},
	{ID: "412", Name: "Thresh", Img: "Thresh.png"},
	{ID: "67", Name: "Vayne", Img: "Vayne.png"},
	{ID: "157", Name: "Yasuo", Img: "Yasuo.png"},
	{ID: "238", Name: "Zed", Img: "Zed.png"},
}

type deps struct{ db *gorm.DB }

func (d *deps) Database(ctx context.Context) *gorm.DB { return d.db.WithContext(ctx) }
func (d *deps) Cron(context.Context) *cron.Cron       { return cron.New() }

type offlineDiscord struct{ s *discordgo.Session }

func (d offlineDiscord) Session() *discordgo.Session { return d.s }

// offlineTransport fails every Discord REST call at once instead of reaching
// the network.
type offlineTransport struct{}

func (offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("discord is offline in loidev")
}

func main() {
	if err := run(); err != nil {
		slog.Error("loidev stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// The config was loaded from .env at package init; pin the values loidev
	// needs and load it again. godotenv never overrides a variable already
	// set, even to "".
	for k, v := range map[string]string{
		"DISCORD_GUILD_ID":   devGuildID,
		"DISCORD_CHANNEL_ID": devChannelID,
		"DISCORD_TOKEN":      "",
		"JWT_SECRET":         "",
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	if os.Getenv("TIMER_TIME") == "" {
		if err := os.Setenv("TIMER_TIME", "10000"); err != nil {
			return err
		}
	}
	config.LoadConfig(os.DevNull)
	logger.InitLogger()
	l := logger.ComponentLogger("loidev")

	db, path, err := openDatabase()
	if err != nil {
		return err
	}
	d := &deps{db: db}

	session, err := discordgo.New("Bot loidev")
	if err != nil {
		return err
	}
	session.Client = &http.Client{Transport: offlineTransport{}}

	gm, err := loi.NewGameManager(d, offlineDiscord{s: session})
	if err != nil {
		return err
	}
	voice, ok := gm.(loi.DevVoice)
	if !ok {
		return errors.New("the game manager does not implement loi.DevVoice")
	}
	players := playersFromEnv()
	voice.DevVoiceSync(players)

	s, err := server.NewServer(gm, swiss.NewService(d))
	if err != nil {
		return err
	}
	api, err := s.Handler()
	if err != nil {
		return err
	}
	root := http.NewServeMux()
	root.Handle("/api/dev/", devRoutes(voice, players))
	root.Handle("/", api)

	addr := os.Getenv("LOIDEV_ADDR")
	if addr == "" {
		addr = ":3001"
	}
	srv := &http.Server{Addr: addr, Handler: root, ReadHeaderTimeout: 15 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	l.Info("loidev listening", "addr", addr, "db", path, "players", strings.Join(sortedIDs(players), ","),
		"timer_time_ms", config.Config().GameManager.TimerTime)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openDatabase opens LOIDEV_DB, or a fresh in-memory database, migrates it
// and seeds what a roll needs.
func openDatabase() (*gorm.DB, string, error) {
	path := os.Getenv("LOIDEV_DB")
	if path == "" {
		path = ":memory:"
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, "", err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, "", err
	}
	// One connection: ":memory:" is a different database per connection, and
	// SQLite serializes writes anyway.
	sqlDB.SetMaxOpenConns(1)
	models := []any{
		&modeldb.Game{}, &modeldb.Player{}, &modeldb.GamePlayer{}, &modeldb.GamePlayerRoll{},
		&modeldb.Champion{}, &modeldb.PlayerChampion{}, &modeldb.WeeklyChampion{},
		&modeldb.LaneRole{}, &modeldb.LeagueVersion{},
	}
	if err := db.AutoMigrate(append(models, swiss.Models()...)...); err != nil {
		return nil, "", err
	}
	roles := modeldb.NewRoleSlice()
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&roles).Error; err != nil {
		return nil, "", err
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&modeldb.LeagueVersion{Version: leagueVersion}).Error; err != nil {
		return nil, "", err
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&champions).Error; err != nil {
		return nil, "", err
	}
	return db, path, nil
}

// playersFromEnv returns the fake voice members, id -> display name.
func playersFromEnv() map[string]string {
	ids := defaultPlayers
	if v := os.Getenv("LOIDEV_PLAYERS"); v != "" {
		ids = strings.Split(v, ",")
	}
	players := make(map[string]string, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			players[id] = displayName(id)
		}
	}
	return players
}

func displayName(id string) string {
	return strings.ToUpper(id[:1]) + id[1:]
}

func sortedIDs(players map[string]string) []string {
	ids := make([]string, 0, len(players))
	for id := range players {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// devRoutes serves POST /api/dev/voice/{id}/join?name=... and
// POST /api/dev/voice/{id}/leave.
func devRoutes(voice loi.DevVoice, players map[string]string) http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/api/dev/voice/{id}/join", func(w http.ResponseWriter, req *http.Request) {
		id := mux.Vars(req)["id"]
		name := req.URL.Query().Get("name")
		if name == "" {
			name = players[id]
		}
		if name == "" {
			name = displayName(id)
		}
		voice.DevVoiceJoin(id, name)
		writeJSON(w, map[string]any{"id": id, "name": name, "inVoice": true})
	}).Methods(http.MethodPost)
	r.HandleFunc("/api/dev/voice/{id}/leave", func(w http.ResponseWriter, req *http.Request) {
		id := mux.Vars(req)["id"]
		voice.DevVoiceLeave(id)
		writeJSON(w, map[string]any{"id": id, "inVoice": false})
	}).Methods(http.MethodPost)
	cors := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{http.MethodPost, http.MethodOptions}),
		handlers.AllowedHeaders([]string{"Content-Type"}),
	)
	return cors(r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
