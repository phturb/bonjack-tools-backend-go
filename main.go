// Program bonjack-tools-backend-go is a backend service for a League of Legends game management tool.
// It provides a REST API, WebSocket communication, and Discord bot integration for managing
// "Loi des Norms" games, player statistics, and champion data.
//
// The service integrates with:
//   - Discord for voice channel monitoring and bot interactions
//   - Riot Games API for champion and game data
//   - PostgreSQL/SQLite for data persistence
//   - WebSocket clients for real-time game updates
//
// Entry point delegates to the cmd package for application initialization and startup.
package main

import (
	"github.com/phturb/bonjack-tools-backend-go/cmd"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
)

func die(d any) {
	l := logger.ComponentLogger("die")
	l.Error("fatal error occurred", "error", d)
	panic(d)
}

func main() {
	if err := cmd.Main(); err != nil {
		die(err)
	}
}
