// Package model provides HTTP API request and response data structures for the bonjack-tools-backend-go service.
// These models define the contract between the HTTP server and web clients for REST API endpoints.
//
// Key structures:
//   - PlayerStats: Player performance statistics for API responses
//   - PlayerChampions: Player-champion relationship data with mastery information
//   - UpdatePlayerChampionPayload: Request payload for updating player champion assignments
//   - Stats: Detailed player statistics including games played, win rates, and performance metrics
//
// Features:
//   - JSON serialization support for HTTP communication
//   - API-specific data transformation from database models
//   - Request validation structures for incoming data
//   - Response formatting for web client consumption
//
// These models are used exclusively by the server package for HTTP API communication
// and are separate from both database persistence models and game logic domain models.
package model

type PlayerStats struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Stats Stats  `json:"stats"`
}

type Stats struct {
	MaxNumberOfRoll int `json:"maxNumberOfRoll"`
	NumberOfGame    int `json:"numberOfGame"`
	TotalRoll       int `json:"totalRoll"`
}
