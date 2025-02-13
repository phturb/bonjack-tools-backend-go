package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"

	"github.com/phturb/bonjack-tools-backend-go/discord"
	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/loi"
	"github.com/phturb/bonjack-tools-backend-go/model"
	modellolapi "github.com/phturb/bonjack-tools-backend-go/model/lolapi"
	"github.com/phturb/bonjack-tools-backend-go/server"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func updateLeagueVersionInDatabase(ctx context.Context, d internal.Dependencies) error {
	slog.Info("updating league of legends versions in database")
	res, err := http.Get("https://ddragon.leagueoflegends.com/api/versions.json")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body) // response body is []byte
	if err != nil {
		return err
	}
	var vers []string
	if err := json.Unmarshal(body, &vers); err != nil { // Parse []byte to go struct pointer
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

	slog.Info("league of legends versions has been updated in database")

	return nil
}

func updateLeagueOfLegendsChampionsInDatabase(ctx context.Context, d internal.Dependencies) error {
	slog.Info("updating league of legends champions in database")
	db := d.Database(ctx)
	var ver model.LeagueVersion
	if err := db.First(&ver).Error; err != nil {
		return err
	}
	res, err := http.Get(fmt.Sprintf("https://ddragon.leagueoflegends.com/cdn/%s/data/en_US/champion.json", ver.Version))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body) // response body is []byte
	if err != nil {
		return err
	}
	type allLeagueOfLegendChampions struct {
		Type    string                          `json:"type"`
		Format  string                          `json:"format"`
		Version string                          `json:"version"`
		Data    map[string]modellolapi.Champion `json:"data"`
	}
	var allLoLChamps allLeagueOfLegendChampions
	if err := json.Unmarshal(body, &allLoLChamps); err != nil {
		return err
	}

	champions := make([]model.Champion, 0, len(allLoLChamps.Data))
	for _, c := range allLoLChamps.Data {
		champions = append(champions, model.Champion{
			Name: c.Name,
			ID:   c.Key,
			Img:  c.Image.Full,
		})
	}

	if err := db.Clauses(clause.OnConflict{
		UpdateAll: true,
	}).Create(&champions).Error; err != nil {
		return err
	}

	slog.Info("league of legends champions has been updated in database")
	return nil
}

func updateWeeklyLeagueOfLegendsChampionsInDatabase(ctx context.Context, d internal.Dependencies) error {
	res, err := http.Get("https://na1.api.riotgames.com/lol/platform/v3/champion-rotations?api_key=" + internal.Config().ApiKeys.RiotApiKey)
	// res, err := http.Get(fmt.Sprintf("https://na1.api.riotgames.com/lol/platform/v3/champion-rotations?api_key=%s", internal.Config.ApiKeys.RiotApiKey))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body) // response body is []byte
	if err != nil {
		return err
	}
	var wcs modellolapi.WeeklyChampion
	if err := json.Unmarshal(body, &wcs); err != nil {
		return err
	}

	wcsdb := make([]model.WeeklyChampion, 0, len(wcs.FreeChampionIds))
	for _, wc := range wcs.FreeChampionIds {
		wcsdb = append(wcsdb, model.WeeklyChampion{
			ID: strconv.Itoa(wc),
		})
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

func upToDate(ctx context.Context, deps internal.Dependencies) error {
	err := updateLeagueVersionInDatabase(ctx, deps)
	if err != nil {
		return err
	}
	err = updateLeagueOfLegendsChampionsInDatabase(ctx, deps)
	if err != nil {
		return err
	}
	err = updateWeeklyLeagueOfLegendsChampionsInDatabase(ctx, deps)
	if err != nil {
		return err
	}
	return nil
}

func die(d interface{}) {
	slog.Error("%v", d)
	panic(d)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	deps, err := internal.NewDependencies(ctx)
	if err != nil {
		die(err)
	}

	go upToDate(ctx, deps)
	_, err = deps.Cron().AddFunc("0,30 * * * *", func() {
		upToDate(ctx, deps)
	})
	if err != nil {
		die(err)
	}
	dm, err := discord.NewDiscordManager()
	if err != nil {
		die(err)
	}

	gm := loi.NewGameManager(deps, dm)
	s, err := server.NewServer(gm)
	if err != nil {
		die(err)
	}

	sErr := make(chan error)
	startServer := func(ctx context.Context) {
		if err := dm.Session().Open(); err != nil {
			sErr <- err
		}
		defer dm.Session().Close()
		sErr <- s.Start(ctx)
	}
	go startServer(ctx)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	select {
	case <-c:
		cancel()
		return
	case err = <-sErr:
		if err != nil {
			slog.Error(err.Error())
		}
		slog.Info("exiting service")
		return
	case <-ctx.Done():
		slog.Info("main context has been closed")
		return
	}
}
