package loi

import (
	"context"
	"fmt"

	"github.com/phturb/bonjack-tools-backend-go/internal"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"

	sharedmodel "github.com/phturb/bonjack-tools-backend-go/model"
	modelserver "github.com/phturb/bonjack-tools-backend-go/model/server"
)

type GameController interface {
	GetPlayers(ctx context.Context) []sharedmodel.Player
	GetGames(ctx context.Context) []sharedmodel.Game
	GetRolls(ctx context.Context) []sharedmodel.GamePlayerRoll
	GetPlayerStats(ctx context.Context, id string) modelserver.PlayerStats
	GetPlayerChampion(ctx context.Context, id string) modelserver.PlayerChampions
	UpdatePlayerChampion(ctx context.Context, id string, entry []modelserver.UpdatePlayerChampionPayload) modelserver.PlayerChampions
}

type gameController struct {
	d internal.Dependencies
}

func (g *gameController) GetPlayers(ctx context.Context) []sharedmodel.Player {
	l := logger.ComponentLogger("GetPlayers")
	players := make([]sharedmodel.Player, 0)
	if out := g.d.Database(ctx).Find(&players); out.Error != nil {
		l.Error("failed to get players from database", "error", out.Error)
		return []sharedmodel.Player{}
	}
	return players
}

func (g *gameController) GetGames(ctx context.Context) []sharedmodel.Game {
	l := logger.ComponentLogger("GetGames")
	games := make([]sharedmodel.Game, 0)
	if out := g.d.Database(ctx).Find(&games); out.Error != nil {
		l.Error("failed to get games from database", "error", out.Error)
		return []sharedmodel.Game{}
	}
	return games
}

func (g *gameController) GetRolls(ctx context.Context) []sharedmodel.GamePlayerRoll {
	l := logger.ComponentLogger("GetRolls")
	gameRolls := make([]sharedmodel.GamePlayerRoll, 0)
	if out := g.d.Database(ctx).Find(&gameRolls); out.Error != nil {
		l.Error("failed to get rolls from database", "error", out.Error)
		return []sharedmodel.GamePlayerRoll{}
	}
	return gameRolls
}

func (g *gameController) GetPlayerChampion(ctx context.Context, id string) modelserver.PlayerChampions {
	l := logger.ComponentLogger("GetPlayerChampion")
	allChampions := make([]sharedmodel.Champion, 0)
	if out := g.d.Database(ctx).Find(&allChampions); out.Error != nil {
		l.Error("failed to get champions from database", "error", out.Error)
		return modelserver.PlayerChampions{}
	}
	playerChampions := make([]sharedmodel.PlayerChampion, 0)
	if out := g.d.Database(ctx).Find(&playerChampions, "player_id = ?", id); out.Error != nil {
		l.Error("failed to get champions from database", "error", out.Error)
		return modelserver.PlayerChampions{}
	}
	var version sharedmodel.LeagueVersion
	if out := g.d.Database(ctx).First(&version); out.Error != nil {
		l.Error("failed to get champions from database", "error", out.Error)
		return modelserver.PlayerChampions{}
	}

	ownedID := make(map[string]struct{}, len(playerChampions))
	for _, champion := range playerChampions {
		ownedID[champion.ChampionID] = struct{}{}
	}

	champions := make(modelserver.PlayerChampions, 0, len(allChampions))
	checkOwned := len(playerChampions) != 0
	for _, champion := range allChampions {
		owned := !checkOwned
		if !owned {
			_, owned = ownedID[champion.ID]
		}
		img := fmt.Sprintf("https://ddragon.leagueoflegends.com/cdn/%s/img/champion/%s", version.Version, champion.Img)
		ac := &modelserver.PlayerChampion{
			ID:    champion.ID,
			Name:  champion.Name,
			Img:   img,
			Owned: owned,
		}
		ac.Img = img
		champions = append(champions, ac)
	}

	return champions
}

func (g *gameController) GetPlayerStats(ctx context.Context, id string) modelserver.PlayerStats {
	l := logger.ComponentLogger("GetPlayerStats")
	var player sharedmodel.Player
	if out := g.d.Database(ctx).First(&player, "id = ?", id); out.Error != nil {
		l.Error("failed to get player from database", "error", out.Error)
		return modelserver.PlayerStats{}
	}

	var playerGames []sharedmodel.GamePlayer
	if out := g.d.Database(ctx).Find(&playerGames, "player_id = ?", id); out.Error != nil {
		l.Error("failed to get game players from database", "error", out.Error)
		return modelserver.PlayerStats{}
	}

	var playerGameRolls []sharedmodel.GamePlayerRoll
	if out := g.d.Database(ctx).Find(&playerGameRolls, "player_id = ?", id); out.Error != nil {
		l.Error("failed to get game player rolls from database", "error", out.Error)
		return modelserver.PlayerStats{}
	}

	maxNumberOfRoll := 0

	maxNumberOfRollMap := map[uint]int{}
	for _, pgr := range playerGameRolls {
		if _, ok := maxNumberOfRollMap[pgr.GameID]; !ok {
			maxNumberOfRollMap[pgr.GameID] = 0
		}
		maxNumberOfRollMap[pgr.GameID] = maxNumberOfRollMap[pgr.GameID] + 1
		if maxNumberOfRollMap[pgr.GameID] > maxNumberOfRoll {
			maxNumberOfRoll = maxNumberOfRollMap[pgr.GameID]
		}
	}

	playerName := ""
	if player.Name != nil {
		playerName = *player.Name
	}

	playerStats := modelserver.PlayerStats{
		ID:   player.ID,
		Name: playerName,
		Stats: modelserver.Stats{
			MaxNumberOfRoll: maxNumberOfRoll,
			NumberOfGame:    len(playerGames),
			TotalRoll:       len(playerGameRolls),
		},
	}

	return playerStats
}

func (g *gameController) UpdatePlayerChampion(ctx context.Context, id string, entry []modelserver.UpdatePlayerChampionPayload) modelserver.PlayerChampions {
	l := logger.ComponentLogger("UpdatePlayerChampion")
	if out := g.d.Database(ctx).Delete(sharedmodel.PlayerChampion{}, "player_id = ?", id); out.Error != nil {
		l.WithError(out.Error).Error("failed to get games from database")
		return modelserver.PlayerChampions{}
	}
	for _, updatedChamp := range entry {
		if !updatedChamp.Owned {
			continue
		}
		if out := g.d.Database(ctx).Model(sharedmodel.PlayerChampion{}).Create(sharedmodel.PlayerChampion{
			PlayerID:   id,
			ChampionID: updatedChamp.ID,
		}); out.Error != nil {
			l.WithError(out.Error).Error("failed to get games from database")
			return modelserver.PlayerChampions{}
		}
	}
	return g.GetPlayerChampion(ctx, id)
}
