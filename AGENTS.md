# AGENTS.md

This file contains guidelines and commands for agentic coding agents working in this Go repository.

## Build/Test Commands

### Core Commands
- `go build .` - Build the main application
- `go run .` - Run the application directly
- `go mod tidy` - Clean up module dependencies
- `go mod download` - Download dependencies

### Testing Commands
- `go test ./...` - Run all tests across all packages
- `go test ./loi -v` - Run tests for the loi package with verbose output
- `go test -run TestE2EGameFlow ./loi` - Run specific test
- `go test -count=1 ./loi` - Run tests without caching
- `go test -race ./...` - Run tests with race detection

### Development Tools
- `air` - Hot reload development (installed via go tools)
- `dlv debug` - Debug with Delve (installed via go tools)

## Code Style Guidelines

### Import Organization
- Group imports in three sections: standard library, third-party, and local packages
- Use blank imports for side effects only when necessary
- Local imports use the module path: `github.com/phturb/bonjack-tools-backend-go/...`

### Naming Conventions
- **Packages**: lowercase, single words when possible (e.g., `loi`, `server`, `model`)
- **Variables**: camelCase, descriptive names
- **Functions**: PascalCase for exported, camelCase for unexported
- **Constants**: UPPER_SNAKE_CASE for exported, camelCase for unexported
- **Interfaces**: PascalCase, often ending with `-er` suffix (e.g., `GameManager`)
- **Structs**: PascalCase, with descriptive field names

### Error Handling
- Always handle errors explicitly, never ignore them
- Use `fmt.Errorf` with `%w` verb for error wrapping
- Return errors as the last return value
- Use structured logging with `slog` for errors and important events

### Logging Patterns
- Use structured logging with `slog` from the standard library
- Initialize logger with `internal.InitLogger()` in `main()`
- Use component-based context: `slog.Info("descriptive message", "component", "componentName")`
- Include relevant structured fields: `slog.Info("user action", "component", "auth", "user_id", userID, "action", "login")`
- Use structured error handling: `slog.Error("operation failed", "component", "api", "error", err, "endpoint", "/users")`

#### Log Configuration
- Environment variables: `LOG_LEVEL` (debug/info/warn/error), `LOG_FORMAT` (json/text)
- Auto-detects format: JSON in production (GO_ENV=production), text in development
- Test logging: Uses text format with debug level for readability

#### Best Practices
- Use descriptive messages, not component names in the message text
- Include context data as structured key-value pairs
- Log errors with structured error field, not embedded in message
- Use consistent field names: `component`, `error`, `user_id`, `guild_id`, `channel_id`

### Struct and Interface Design
- Define interfaces for behavior that needs to be mocked or tested
- Use composition over inheritance
- Implement interfaces with concrete types, don't export interfaces unless needed
- Use dependency injection pattern via interfaces

### Database Patterns
- Use GORM as the ORM with PostgreSQL/SQLite
- Define models with proper GORM tags
- Use transactions for multi-step operations
- Handle foreign key relationships properly
- Use `clause.OnConflict` for upsert operations

### Concurrency Patterns
- Use `sync.RWMutex` for protecting shared state
- Use channels for communication between goroutines
- Always lock before accessing shared data, unlock with defer
- Use context for cancellation and timeouts

### HTTP/WebSocket Patterns
- Use Gorilla Mux for routing
- Use Gorilla WebSocket for WebSocket connections
- Handle CORS properly with `handlers.CORS`
- Use structured JSON responses
- Implement proper error status codes

### Testing Patterns
- Use testify for assertions and mocking
- Create test setup functions with `setupTest(t *testing.T)`
- Use table-driven tests for multiple scenarios
- Mock external dependencies (Discord, Database)
- Use in-memory SQLite for database tests
- Include end-to-end tests for critical flows

### Configuration Management
- Use environment variables with `.env` files
- Load configuration via `internal.LoadConfig()`
- Use singleton pattern for config access
- Provide sensible defaults for optional settings

### Discord Integration
- Use discordgo for Discord API interactions
- Handle Discord events with proper event handlers
- Use deferred responses for interactions
- Manage application commands properly

### JSON/API Patterns
- Use struct tags for JSON serialization
- Handle JSON unmarshaling errors properly
- Use pointer types for optional JSON fields
- Validate input data before processing

## Project Structure

```
/                    # Root
├── main.go         # Application entry point
├── go.mod          # Module definition
├── internal/       # Internal packages
│   ├── config.go   # Configuration management
│   └── dependencies.go # Dependency injection
├── loi/            # Game logic package
│   ├── game_manager.go
│   ├── game_manager_test.go
│   └── model/
├── server/         # HTTP server package
├── discord/        # Discord integration
├── model/          # Database models
│   ├── model.go
│   ├── lolapi/
│   ├── server/
│   └── websocket/
└── .env            # Environment variables
```

## Key Dependencies

- **GORM**: Database ORM (`gorm.io/gorm`)
- **Gorilla**: HTTP routing and WebSockets (`github.com/gorilla/mux`, `github.com/gorilla/websocket`)
- **Discordgo**: Discord API (`github.com/bwmarrin/discordgo`)
- **Testify**: Testing utilities (`github.com/stretchr/testify`)
- **Cron**: Scheduled tasks (`github.com/robfig/cron/v3`)
- **JWT**: Authentication (`github.com/auth0/go-jwt-middleware/v2`)

## Development Notes

- This is a backend service for a League of Legends game management tool
- Integrates with Discord for voice channel monitoring
- Uses WebSocket for real-time communication with web clients
- Implements a game state manager for "Loi des Norms" game logic
- Database stores players, games, champions, and statistics
- Uses structured logging throughout the codebase

## Common Patterns to Follow

1. **Dependency Injection**: Use the `internal.Dependencies` interface for accessing database, cron, etc.
2. **Context Management**: Pass context through the call chain for cancellation and database operations
3. **Error Logging**: Always log errors with context using structured logging
4. **State Management**: Use proper mutex locking for shared state modifications
5. **Database Transactions**: Use transactions for multi-step database operations
6. **Testing**: Mock external dependencies and use in-memory databases for tests