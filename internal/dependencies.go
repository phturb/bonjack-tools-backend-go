package internal

import (
	"context"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

type Dependencies interface {
	Database(ctx context.Context) *gorm.DB
	Cron() *cron.Cron
}
