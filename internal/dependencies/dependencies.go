// Package dependencies provides concrete dependency injection container implementation for the bonjack-tools-backend-go service.
// It creates, configures, and manages the lifecycle of core application dependencies.
//
// Key responsibilities:
//   - Database connection establishment and configuration
//   - Database migration execution and schema management
//   - Initial data seeding for application startup
//   - Cron scheduler instance creation and management
//   - Dependency container initialization with proper resource management
//
// Database management:
//   - PostgreSQL and SQLite driver support
//   - Connection pooling and timeout configuration
//   - Auto-migration for all GORM models
//   - Transaction management and connection lifecycle
//   - Data seeding for lane roles and reference data
//
// Dependency injection:
//   - Implements the internal.Dependencies interface
//   - Provides concrete implementations for all dependencies
//   - Manages resource creation and cleanup
//   - Ensures proper initialization order
//
// Integration points:
//   - Configuration: Uses config package for connection settings
//   - Database: Works with all model packages for migrations
//   - Cron: Provides scheduler for background tasks
//   - Logging: Structured logging for all operations
//
// This package serves as the concrete implementation of the dependency injection system,
// ensuring all core services are properly initialized and available to the application.
package dependencies

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

type dependencies struct {
	db *gorm.DB
	c  *cron.Cron
}

func instantiateDB(ctx context.Context) (*gorm.DB, error) {
	l := logger.ComponentLogger("deps")
	conf := config.Config()
	sslmode := "disable"
	if conf.Database.SSL == "true" {
		sslmode = "enable"
	}

	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s timezone=America/Toronto", conf.Database.Host, conf.Database.Username, conf.Database.Password, conf.Database.DatabaseName, conf.Database.Port, sslmode)
	if len(conf.Database.DatabaseURL) > 0 {
		dsn = conf.Database.DatabaseURL
	}
	// db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	// db, err := gorm.Open(sqlite.Open("gorm.db"), &gorm.Config{})
	l.Info("initializing database connection")

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: false,
		Logger: gormlogger.NewSlogLogger(slog.Default(), gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Info,
			IgnoreRecordNotFoundError: false,
			Colorful:                  true,
		}),
	})
	if err != nil {
		return nil, err
	}

	mSessDB := db.WithContext(ctx)

	slog.Info("[deps] - executing database auto migration")
	err = mSessDB.AutoMigrate(
		&modeldb.Game{},
		&modeldb.Player{},
		&modeldb.GamePlayerRoll{},
		&modeldb.Champion{},
		&modeldb.WeeklyChampion{},
		&modeldb.LaneRole{},
		&modeldb.LeagueVersion{},
	)
	if err != nil {
		return nil, err
	}

	laneRoles := modeldb.NewRoleSlice()
	if err := mSessDB.Clauses(clause.OnConflict{
		UpdateAll: true,
	}).Model(&modeldb.LaneRole{}).Create(&laneRoles).Error; err != nil {
		l.Error("failed to seed lane role in database")
	}

	return db, nil
}

func instantitateCron(_ context.Context) (*cron.Cron, error) {
	l := logger.ComponentLogger("deps")
	l.Info("initializing cron scheduler")
	return cron.New(cron.WithLogger(&cronlogger{logger.DefaultLogger()})), nil
}

func NewDependencies(ctx context.Context) (internal.Dependencies, error) {
	l := logger.ComponentLogger("deps")
	l.Info("creating dependencies")

	db, err := instantiateDB(ctx)
	if err != nil {
		return nil, err
	}

	c, err := instantitateCron(ctx)
	if err != nil {
		return nil, err
	}

	return &dependencies{
		db: db,
		c:  c,
	}, nil
}

func (d *dependencies) Database(ctx context.Context) *gorm.DB {
	return d.db.WithContext(ctx)
}

func (d *dependencies) Cron(_ context.Context) *cron.Cron {
	return d.c
}

type cronlogger struct {
	*logger.Logger
}

func (l *cronlogger) Error(err error, msg string, args ...any) {
	l.Logger.WithError(err).Error(msg, args...)
}
