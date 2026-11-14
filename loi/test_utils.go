package loi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestDatabase provides a consistent in-memory database setup for tests
type TestDatabase struct {
	DB *gorm.DB
}

// NewTestDatabase creates a new in-memory SQLite database with all tables migrated
func NewTestDatabase(t testing.TB) *TestDatabase {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		if t != nil {
			assert.NoError(t, err, "Failed to create in-memory database")
		} else {
			t.Log("Failed to create in-memory database", "error", err)
			panic(err)
		}
	}

	// Auto-migrate all required models
	err = db.AutoMigrate(
		&modeldb.Game{},
		&modeldb.Player{},
		&modeldb.GamePlayer{},
		&modeldb.GamePlayerRoll{},
		&modeldb.Champion{},
		&modeldb.PlayerChampion{},
		&modeldb.WeeklyChampion{},
		&modeldb.LaneRole{},
		&modeldb.LeagueVersion{},
	)
	if err != nil {
		if t != nil {
			assert.NoError(t, err, "Failed to migrate database schema")
		} else {
			t.Error("Failed to migrate database schema", "error", err)
			panic(err)
		}
	}

	return &TestDatabase{DB: db}
}

// SeedTestData creates basic test data in the database
func (td *TestDatabase) SeedTestData(t assert.TestingT) {
	// Create test players
	players := []modeldb.Player{
		{ID: "player1", Name: stringPtr("Player 1")},
		{ID: "player2", Name: stringPtr("Player 2")},
		{ID: "player3", Name: stringPtr("Player 3")},
		{ID: "player4", Name: stringPtr("Player 4")},
		{ID: "player5", Name: stringPtr("Player 5")},
	}

	for _, player := range players {
		err := td.DB.Create(&player).Error
		if err != nil && t != nil {
			// Ignore unique constraint errors for re-runs
			if !isUniqueConstraintError(err) {
				assert.NoError(t, err)
			}
		}
	}

	// Seed league version
	err := td.DB.Create(&modeldb.LeagueVersion{Version: "14.1.1"}).Error
	if err != nil && t != nil {
		if !isUniqueConstraintError(err) {
			assert.NoError(t, err)
		}
	}

	// Seed champions
	champions := []modeldb.Champion{
		{ID: "1", Name: "Ashe", Img: "Ashe.png"},
		{ID: "2", Name: "Garen", Img: "Garen.png"},
		{ID: "3", Name: "Ryze", Img: "Ryze.png"},
		{ID: "4", Name: "Annie", Img: "Annie.png"},
		{ID: "5", Name: "Warwick", Img: "Warwick.png"},
	}

	for _, champion := range champions {
		err := td.DB.Create(&champion).Error
		if err != nil && t != nil {
			if !isUniqueConstraintError(err) {
				assert.NoError(t, err)
			}
		}
	}
}

// isUniqueConstraintError checks if error is a unique constraint violation
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	// SQLite unique constraint error
	return err.Error() == "UNIQUE constraint failed: champions.id" ||
		err.Error() == "UNIQUE constraint failed: players.id"
}

// MockDiscordSession wraps discordgo.Session to override methods that cause panics in tests
type MockDiscordSession struct {
	*discordgo.Session
	shouldPanic bool
}

func (m *MockDiscordSession) ApplicationCommandCreate(applicationID, guildID string, cmd *discordgo.ApplicationCommand, options ...discordgo.RequestOption) (*discordgo.ApplicationCommand, error) {
	if m.shouldPanic {
		panic("mock panic for testing")
	}
	return &discordgo.ApplicationCommand{
		ID:   "mock-cmd-id",
		Name: cmd.Name,
	}, nil
}

func (m *MockDiscordSession) ChannelMessageEditComplex(edit *discordgo.MessageEdit) (*discordgo.Message, error) {
	if m.shouldPanic {
		panic("mock panic for testing")
	}
	return &discordgo.Message{
		ID:      edit.ID,
		Content: *edit.Content,
	}, nil
}

func (m *MockDiscordSession) InteractionResponseEdit(interaction *discordgo.Interaction, edit *discordgo.WebhookEdit) (*discordgo.Message, error) {
	if m.shouldPanic {
		panic("mock panic for testing")
	}
	return &discordgo.Message{
		ID: "mock-interaction-response-id",
	}, nil
}

func (m *MockDiscordSession) InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse) error {
	if m.shouldPanic {
		panic("mock panic for testing")
	}
	return nil
}

type MockDiscordManager struct {
	session *MockDiscordSession
}

func (m *MockDiscordManager) Session() *discordgo.Session {
	return m.session.Session
}

// MockDependenciesWithDB provides mock dependencies with an in-memory database
type MockDependenciesWithDB struct {
	db *gorm.DB
}

func NewMockDependenciesWithDB(t testing.TB) *MockDependenciesWithDB {
	testDB := NewTestDatabase(t)
	testDB.SeedTestData(t)

	return &MockDependenciesWithDB{
		db: testDB.DB,
	}
}

func (m *MockDependenciesWithDB) Database(ctx context.Context) *gorm.DB {
	return m.db
}

func (m *MockDependenciesWithDB) Cron(_ context.Context) *cron.Cron {
	// Return a new cron instance for tests that don't need cron
	return cron.New()
}

// FakeDiscordAPI stands in for the Discord REST API in tests. It answers
// every call with 200 and a message object with a new id, and records the
// calls, so interaction responses and message edits run without network.
type FakeDiscordAPI struct {
	mu    sync.Mutex
	calls []FakeDiscordCall
}

// FakeDiscordCall is one request received by FakeDiscordAPI.
type FakeDiscordCall struct {
	Method string
	Path   string
	Body   string
}

func (f *FakeDiscordAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, FakeDiscordCall{Method: r.Method, Path: r.URL.Path, Body: string(body)})
	id := fmt.Sprintf("fake-message-%d", len(f.calls))
	f.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":%q,"channel_id":"test-channel"}`, id))),
		Request:    r,
	}, nil
}

// Calls returns a copy of the calls received so far.
func (f *FakeDiscordAPI) Calls() []FakeDiscordCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeDiscordCall(nil), f.calls...)
}

// FakeDiscordAPIOf returns the fake REST API behind a session made by
// CreateMockDiscordSession.
func FakeDiscordAPIOf(s *discordgo.Session) *FakeDiscordAPI {
	f, _ := s.Client.Transport.(*FakeDiscordAPI)
	return f
}

// CreateMockDiscordSession creates a properly mocked Discord session for tests.
// Its REST calls go to a FakeDiscordAPI, never to the network.
func CreateMockDiscordSession(t testing.TB) *discordgo.Session {
	mockSession, err := discordgo.New("mock-token")
	if err != nil {
		if t != nil {
			assert.NoError(t, err, "Failed to create mock discord session")
		} else {
			panic(err)
		}
	}

	mockSession.State = discordgo.NewState()
	mockSession.State.User = &discordgo.User{}
	mockSession.Client = &http.Client{Transport: &FakeDiscordAPI{}}

	return mockSession
}

// SetupTestEnvironment provides a complete test environment setup
func SetupTestEnvironment(t testing.TB) (*gameManager, *MockDiscordManager, *MockDependenciesWithDB) {
	// Load test config. The guild and channel ids are the ones the tests use;
	// setting them here makes the tests pass from a clean clone, with no
	// .env.test file and nothing exported in the shell.
	t.Setenv("DISCORD_GUILD_ID", "test-guild")
	t.Setenv("DISCORD_CHANNEL_ID", "test-channel")
	config.LoadConfig("../.env.test")

	// Create mock Discord components
	mockSession := CreateMockDiscordSession(t)

	mockDM := &MockDiscordManager{
		session: &MockDiscordSession{
			Session:     mockSession,
			shouldPanic: false,
		},
	}

	// Create mock dependencies with database
	mockDeps := NewMockDependenciesWithDB(t)

	// Create the game manager
	rawGM, err := NewGameManager(mockDeps, mockDM)
	require.NoError(t, err, "Failed to create game manager")
	gm := rawGM.(*gameManager)

	// Set test logger - use text format for readability during tests
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Simulate the onDiscordReady event to set the guild ID
	ready := &discordgo.Ready{
		Guilds: []*discordgo.Guild{
			{
				ID:   "test-guild",
				Name: "Test Guild",
			},
		},
	}

	gm.onDiscordReady(mockDM.Session(), ready)

	return gm, mockDM, mockDeps
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}
