// Package lolapi provides Riot Games API integration for League of Legends data access.
// It offers a client interface for fetching game versions, champion information, and weekly rotations.
//
// Key features:
//   - League of Legends version fetching from Data Dragon API
//   - Champion data retrieval including names, titles, and metadata
//   - Weekly champion rotation information from Riot API
//   - HTTP client with proper error handling and context support
//   - Automatic retry and timeout handling for API requests
//
// API endpoints:
//   - Data Dragon API: Version lists, champion data, and game assets
//   - Riot Games API: Weekly champion rotations and platform-specific data
//   - Regional endpoints with proper routing for different Riot servers
//
// Data synchronization:
//   - Periodic updates through cron jobs for champion data
//   - Version checking to maintain current game data
//   - Database synchronization for offline access and caching
//
// The service integrates with the database layer to store and retrieve champion information,
// enabling the game system to work with current League of Legends data.
package lolapi

import (
	"context"

	modeldb "github.com/phturb/bonjack-tools-backend-go/model/db"
)

type Client interface {
	GetLeagueOfLegendsVersions(ctx context.Context) ([]string, error)
	GetLeagueOfLegendsVersionChampions(ctx context.Context, ver string) ([]modeldb.Champion, error)
	GetLeagueOfLegendsWeeklyChampions(ctx context.Context) ([]modeldb.WeeklyChampion, error)
}
