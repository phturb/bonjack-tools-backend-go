// Package cron provides scheduled background task management for the bonjack-tools-backend-go service.
// It handles periodic data synchronization and maintenance tasks using cron scheduling.
//
// Key features:
//   - Periodic League of Legends API data synchronization
//   - Champion data updates and version management
//   - Weekly champion rotation synchronization
//   - Extensible cron job framework with interface-based design
//   - Database updates and data consistency maintenance
//
// Scheduled tasks:
//   - League of Legends version checking and updates
//   - Champion data refresh from Data Dragon API
//   - Weekly champion rotation updates from Riot API
//   - Database cleanup and maintenance operations
//
// Integration points:
//   - lolapi service: External API data fetching
//   - Database layer: Data persistence and updates
//   - Cron scheduler: Job execution and timing
//   - Logging: Task execution monitoring and error reporting
//
// The package ensures that the game system operates with current League of Legends data
// and maintains data consistency between external APIs and the local database.
package cron

import (
	"context"
	"errors"

	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
	"github.com/phturb/bonjack-tools-backend-go/service/lolapi"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type lolAPICron struct {
	ctx context.Context
	d   internal.Dependencies
	c   lolapi.Client
}

func NewLolAPICron(ctx context.Context, d internal.Dependencies, c lolapi.Client) (CronExecutable, error) {
	return &lolAPICron{
		d:   d,
		c:   c,
		ctx: ctx,
	}, nil
}

func (cs *lolAPICron) updateLeagueVersionInDatabase(ctx context.Context) error {
	l := logger.ComponentLogger("updateLeagueVersionInDatabase")
	l.Info("updating league of legends versions in database")
	vers, err := cs.c.GetLeagueOfLegendsVersions(ctx)
	if err != nil {
		return err
	}
	if len(vers) <= 0 {
		return errors.New("league of legends versions result is empty, unable to identify latest version")
	}
	err = cs.d.Database(ctx).Transaction(func(tx *gorm.DB) error {
		var oldVersions []modeldb.LeagueVersion
		if err := tx.Find(&oldVersions).Error; err != nil {
			return err
		}
		if len(oldVersions) > 0 {
			if err := tx.Delete(&oldVersions).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(modeldb.LeagueVersion{Version: vers[0]}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	l.Info("league of legends versions updated in database")

	return nil
}

func (cs *lolAPICron) updateLeagueOfLegendsChampionsInDatabase(ctx context.Context) error {
	l := logger.ComponentLogger("updateLeagueOfLegendsChampionsInDatabase")
	l.Info("updating league of legends champions in database")
	db := cs.d.Database(ctx)
	var ver modeldb.LeagueVersion
	if err := db.First(&ver).Error; err != nil {
		return err
	}

	champions, err := cs.c.GetLeagueOfLegendsVersionChampions(ctx, ver.Version)
	if err != nil {
		return err
	}

	if err := db.Clauses(clause.OnConflict{
		UpdateAll: true,
	}).Create(&champions).Error; err != nil {
		return err
	}

	l.Info("league of legends champions updated in database")
	return nil
}

func (cs *lolAPICron) updateWeeklyLeagueOfLegendsChampionsInDatabase(ctx context.Context) error {
	wcsdb, err := cs.c.GetLeagueOfLegendsWeeklyChampions(ctx)
	if err != nil {
		return err
	}

	if err := cs.d.Database(ctx).Transaction(func(tx *gorm.DB) error {
		var oldWcsdb []modeldb.WeeklyChampion
		if err := tx.Find(&oldWcsdb).Error; err != nil {
			return err
		}
		if len(oldWcsdb) > 0 {
			if err := tx.Delete(&oldWcsdb).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&wcsdb).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (cs *lolAPICron) Run() {
	l := logger.ComponentLogger("upToDate")
	l.Info("updating league of legends champions in database")
	err := cs.updateLeagueVersionInDatabase(cs.ctx)
	if err != nil {
		panic(err.Error())
	}
	err = cs.updateLeagueOfLegendsChampionsInDatabase(cs.ctx)
	if err != nil {
		panic(err.Error())
	}
	err = cs.updateWeeklyLeagueOfLegendsChampionsInDatabase(cs.ctx)
	if err != nil {
		panic(err.Error())
	}
	return
}

func (cs *lolAPICron) JobSpec() string {
	return "0,30 * * * *"
}
