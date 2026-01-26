// Package cmd provides application bootstrap and orchestration for the bonjack-tools-backend-go service.
// It handles initialization of all core components including logging, dependencies, Discord integration,
// game management, HTTP server, and scheduled tasks.
//
// Key responsibilities:
//   - Initialize structured logging and configuration
//   - Create and wire dependency injection container
//   - Set up Discord bot session and event handlers
//   - Configure game manager with Discord and database dependencies
//   - Start HTTP server with WebSocket support
//   - Schedule and run background cron jobs
//   - Handle graceful shutdown and signal management
//
// The Main function serves as the application entry point, coordinating all subsystems
// and managing the complete application lifecycle.
package cmd

import (
	"context"
	"os"
	"os/signal"

	"github.com/phturb/bonjack-tools-backend-go/cron"
	"github.com/phturb/bonjack-tools-backend-go/discord"
	"github.com/phturb/bonjack-tools-backend-go/internal/dependencies"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
	"github.com/phturb/bonjack-tools-backend-go/loi"
	"github.com/phturb/bonjack-tools-backend-go/server"
	"github.com/phturb/bonjack-tools-backend-go/service/lolapi"
)

func Main() error {
	// Initialize structured logging
	logger.InitLogger()
	l := logger.ComponentLogger("main")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l.Info("initializing dependencies")
	deps, err := dependencies.NewDependencies(ctx)
	if err != nil {
		return err
	}

	l.Info("creating Lol API Client")
	c, err := lolapi.NewClient()
	if err != nil {
		return err
	}

	l.Info("creating Lol API Cron executable")
	lolAPISync, err := cron.NewLolAPICron(ctx, deps, c)
	if err != nil {
		return err
	}

	l.Info("creating discord manager")
	dm, err := discord.NewDiscordManager()
	if err != nil {
		return err
	}

	l.Info("creating game manager")
	gm, err := loi.NewGameManager(deps, dm)
	if err != nil {
		return err
	}

	l.Info("creating server")
	s, err := server.NewServer(gm)
	if err != nil {
		return err
	}

	go lolAPISync.Run()
	_, err = deps.Cron(ctx).AddJob(lolAPISync.JobSpec(), lolAPISync)
	if err != nil {
		return err
	}

	dErr := make(chan error)
	sErr := make(chan error)
	go func(ctx context.Context) {
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
	}(ctx)

	cs := make(chan os.Signal, 1)
	signal.Notify(cs, os.Interrupt)
	select {
	case <-cs:
		l.Warn("shutting down the service")
		srv, _ := s.GetHTTPServer()
		if srv != nil {
			srv.Shutdown(ctx)
		}
		cancel()
		return nil
	case err = <-dErr:
		if err != nil {
			l.Error("discord error occurred", "error", err)
		}
		l.Info("exiting service")
		cancel()
		return nil
	case err = <-sErr:
		if err != nil {
			l.Error("server error occurred", "error", err)
		}
		l.Info("exiting service")
		cancel()
		return nil
	case <-ctx.Done():
		l.Info("main context has been closed")
		return nil
	}
}
