package lolapi

import (
	"context"

	"github.com/phturb/bonjack-tools-backend-go/model"
)

type Client interface {
	GetLeagueOfLegendsVersions(ctx context.Context) ([]string, error)
	GetLeagueOfLegendsVersionChampions(ctx context.Context, ver string) ([]model.Champion, error)
	GetLeagueOfLegendsWeeklyChampions(ctx context.Context) ([]model.WeeklyChampion, error)
}
