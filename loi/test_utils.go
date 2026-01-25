package loi

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	sharedmodel "github.com/phturb/bonjack-tools-backend-go/model"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
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
		&sharedmodel.Game{},
		&sharedmodel.Player{},
		&sharedmodel.GamePlayer{},
		&sharedmodel.GamePlayerRoll{},
		&sharedmodel.Champion{},
		&sharedmodel.PlayerChampion{},
		&sharedmodel.WeeklyChampion{},
		&sharedmodel.LaneRole{},
		&sharedmodel.LeagueVersion{},
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
	players := []sharedmodel.Player{
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
	err := td.DB.Create(&sharedmodel.LeagueVersion{Version: "14.1.1"}).Error
	if err != nil && t != nil {
		if !isUniqueConstraintError(err) {
			assert.NoError(t, err)
		}
	}

	// Seed champions
	champions := []sharedmodel.Champion{
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
	discordgo.Session
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
	return &m.session.Session
}

func (m *MockDiscordManager) GetConfigChannel() (*discordgo.Channel, error) {
	return &discordgo.Channel{
		ID:   "mock-channel-id",
		Name: "mock-channel",
	}, nil
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

func (m *MockDependenciesWithDB) Cron() *cron.Cron {
	// Return a new cron instance for tests that don't need cron
	return cron.New()
}

// CreateMockDiscordSession creates a properly mocked Discord session for tests
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

	return mockSession
}

// SetupTestEnvironment provides a complete test environment setup
func SetupTestEnvironment(t testing.TB) (*gameManager, *MockDiscordManager, *MockDependenciesWithDB) {
	// Load test config
	internal.LoadConfig("../.env.test")

	// Create mock Discord components
	mockSession := CreateMockDiscordSession(t)

	mockDM := &MockDiscordManager{
		session: &MockDiscordSession{
			Session:     *mockSession,
			shouldPanic: false,
		},
	}

	// Create mock dependencies with database
	mockDeps := NewMockDependenciesWithDB(t)

	// Create the game manager
	gm := NewGameManager(mockDeps, mockDM).(*gameManager)

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
