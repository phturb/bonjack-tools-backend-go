// Package internal provides core dependency injection interfaces for the bonjack-tools-backend-go service.
// It defines the contract for accessing shared application resources and services.
//
// Key interface:
//   - Dependencies: Core dependency injection contract providing access to database and scheduler
//
// Provided resources:
//   - Database: GORM database connection for data persistence
//   - Cron: Job scheduler for background task execution
//   - Context management for operation lifecycle
//
// Design pattern:
//   - Dependency injection for loose coupling between components
//   - Interface-based design for testability and modularity
//   - Centralized resource management and access control
//
// Usage:
//   - Passed to components during initialization
//   - Provides controlled access to core application services
//   - Enables easy mocking for unit testing
//   - Ensures consistent resource usage across the application
//
// This package forms the foundation for the application's dependency management system,
// promoting clean architecture and separation of concerns.
package internal

import (
	"context"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

type Dependencies interface {
	Database(ctx context.Context) *gorm.DB
	Cron(ctx context.Context) *cron.Cron
}
