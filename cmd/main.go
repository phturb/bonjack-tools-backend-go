package cmd

import (
	"context"
	"errors"
	"os"
	"os/signal"

	"github.com/phturb/bonjack-tools-backend-go/discord"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/internal/dependencies"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
	"github.com/phturb/bonjack-tools-backend-go/loi"
	"github.com/phturb/bonjack-tools-backend-go/model"
	"github.com/phturb/bonjack-tools-backend-go/server"
	"github.com/phturb/bonjack-tools-backend-go/service/lolapi"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func updateLeagueVersionInDatabase(ctx context.Context, d internal.Dependencies, c lolapi.Client) error {
	l := logger.ComponentLogger("updateLeagueVersionInDatabase")
	l.Info("updating league of legends versions in database")
	vers, err := c.GetLeagueOfLegendsVersions(ctx)
	if err != nil {
		return err
	}
	if len(vers) <= 0 {
		return errors.New("league of legends versions result is empty, unable to identify latest version")
	}
	err = d.Database(ctx).Transaction(func(tx *gorm.DB) error {
		var oldVersions []model.LeagueVersion
		if err := tx.Find(&oldVersions).Error; err != nil {
			return err
		}
		if len(oldVersions) > 0 {
			if err := tx.Delete(&oldVersions).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(model.LeagueVersion{Version: vers[0]}).Error; err != nil {
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

func updateLeagueOfLegendsChampionsInDatabase(ctx context.Context, d internal.Dependencies, c lolapi.Client) error {
	l := logger.ComponentLogger("updateLeagueOfLegendsChampionsInDatabase")
	l.Info("updating league of legends champions in database")
	db := d.Database(ctx)
	var ver model.LeagueVersion
	if err := db.First(&ver).Error; err != nil {
		return err
	}

	champions, err := c.GetLeagueOfLegendsVersionChampions(ctx, ver.Version)
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

func updateWeeklyLeagueOfLegendsChampionsInDatabase(ctx context.Context, d internal.Dependencies, c lolapi.Client) error {
	wcsdb, err := c.GetLeagueOfLegendsWeeklyChampions(ctx)
	if err != nil {
		return err
	}

	if err := d.Database(ctx).Transaction(func(tx *gorm.DB) error {
		var oldWcsdb []model.WeeklyChampion
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

func upToDate(ctx context.Context, deps internal.Dependencies, c lolapi.Client) error {
	l := logger.ComponentLogger("upToDate")
	l.Info("updating league of legends champions in database")
	err := updateLeagueVersionInDatabase(ctx, deps, c)
	if err != nil {
		return err
	}
	err = updateLeagueOfLegendsChampionsInDatabase(ctx, deps, c)
	if err != nil {
		return err
	}
	err = updateWeeklyLeagueOfLegendsChampionsInDatabase(ctx, deps, c)
	if err != nil {
		return err
	}
	return nil
}

func die(d any) {
	l := logger.ComponentLogger("die")
	l.Error("fatal error occurred", "error", d)
	panic(d)
}

func Main() {
	// Initialize structured logging
	logger.InitLogger()
	l := logger.ComponentLogger("main")

	ctx, cancel := context.WithCancel(context.Background())

	deps, err := dependencies.NewDependencies(ctx)
	if err != nil {
		die(err)
	}

	c := lolapi.NewClient()

	go upToDate(ctx, deps, c)
	_, err = deps.Cron().AddFunc("0,30 * * * *", func() {
		upToDate(ctx, deps, c)
	})
	if err != nil {
		die(err)
	}
	l.Info("creating discord manager")
	dm, err := discord.NewDiscordManager()
	if err != nil {
		die(err)
	}

	l.Info("creating game manager")
	gm := loi.NewGameManager(deps, dm)
	l.Info("creating server")
	s, err := server.NewServer(gm)
	if err != nil {
		die(err)
	}

	dErr := make(chan error)
	sErr := make(chan error)
	startServer := func(ctx context.Context) {
		l.Info("starting discord session")
		if err := dm.Session().Open(); err != nil {
			dErr <- err
			return
		}
		defer dm.Session().Close()
		l.Info("starting server")
		sch := s.Start(ctx)
		select {
		case <-ctx.Done():
			sErr <- nil
			return
		case err := <-sch:
			sErr <- err
			return
		}
	}
	go startServer(ctx)

	cs := make(chan os.Signal, 1)
	signal.Notify(cs, os.Interrupt)
	select {
	case <-cs:
		srv, _ := s.GetHTTPServer()
		if srv != nil {
			srv.Shutdown(ctx)
		}
		cancel()
		return
	case err = <-dErr:
		if err != nil {
			l.Error("discord error occurred", "error", err)
		}
		l.Info("exiting service")
		cancel()
		return
	case err = <-sErr:
		if err != nil {
			l.Error("server error occurred", "error", err)
		}
		l.Info("exiting service")
		cancel()
		return
	case <-ctx.Done():
		l.Info("main context has been closed")
		return
	}
}
