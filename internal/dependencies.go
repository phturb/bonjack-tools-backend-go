package internal

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/phturb/bonjack-tools-backend-go/model"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type dependencies struct {
	db *gorm.DB
	c  *cron.Cron
}

type Dependencies interface {
	Database(ctx context.Context) *gorm.DB
	Cron() *cron.Cron
}

func NewDependencies(ctx context.Context) (Dependencies, error) {
	slog.Info("creating dependencies")
	// db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	slog.Info("initializing database connection")
	conf := Config()
	sslmode := "disable"
	if conf.Database.SSL == "true" {
		sslmode = "enable"
	}

	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s timezone=EST", conf.Database.Host, conf.Database.Username, conf.Database.Password, conf.Database.DatabaseName, conf.Database.Port, sslmode)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, err
	}
	slog.Info("executing database auto migration")
	err = db.AutoMigrate(&model.Game{},
		&model.Player{},
		&model.GamePlayer{},
		&model.GamePlayerRoll{},
		&model.Champion{},
		&model.PlayerChampion{},
		&model.WeeklyChampion{},
		&model.LaneRole{},
		&model.LeagueVersion{},
	)
	if err != nil {
		return nil, err
	}

	c := cron.New()

	return &dependencies{
		db: db,
		c:  c,
	}, nil
}

func (d *dependencies) Database(ctx context.Context) *gorm.DB {
	return d.db.WithContext(ctx)
}

func (d *dependencies) Cron() *cron.Cron {
	return d.c
}
