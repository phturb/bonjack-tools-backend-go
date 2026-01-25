package lolapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/phturb/bonjack-tools-backend-go/internal/config"
	"github.com/phturb/bonjack-tools-backend-go/model"
)

const (
	ddragonBaseURL                 = "https://ddragon.leagueoflegends.com"
	riotGameLeagueOfLegendsBaseURL = "https://na1.api.riotgames.com"
)

type client struct {
	cl *http.Client
}

func NewClient() Client {
	return &client{
		cl: &http.Client{},
	}
}

func (c *client) GetLeagueOfLegendsVersions(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ddragonBaseURL+"/api/versions.json", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body) // response body is []byte
	if err != nil {
		return nil, err
	}
	var vers []string
	if err := json.Unmarshal(body, &vers); err != nil { // Parse []byte to go struct pointer
		return nil, err
	}
	return vers, nil
}

func (c *client) GetLeagueOfLegendsVersionChampions(ctx context.Context, ver string) ([]model.Champion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ddragonBaseURL+fmt.Sprintf("/cdn/%s/data/en_US/champion.json", ver), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body) // response body is []byte
	if err != nil {
		return nil, err
	}
	type allLeagueOfLegendChampions struct {
		Type    string              `json:"type"`
		Format  string              `json:"format"`
		Version string              `json:"version"`
		Data    map[string]champion `json:"data"`
	}
	var allLoLChamps allLeagueOfLegendChampions
	if err := json.Unmarshal(body, &allLoLChamps); err != nil {
		return nil, err
	}

	champions := make([]model.Champion, 0, len(allLoLChamps.Data))
	for _, c := range allLoLChamps.Data {
		champions = append(champions, model.Champion{
			Name: c.Name,
			ID:   c.Key,
			Img:  c.Image.Full,
		})
	}

	return champions, nil
}

func (c *client) GetLeagueOfLegendsWeeklyChampions(ctx context.Context) ([]model.WeeklyChampion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, riotGameLeagueOfLegendsBaseURL+fmt.Sprintf("/lol/platform/v3/champion-rotations?api_key=%s", config.Config().APIKeys.RiotAPIKey), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body) // response body is []byte
	if err != nil {
		return nil, err
	}
	var wcs weeklyChampion
	if err := json.Unmarshal(body, &wcs); err != nil {
		return nil, err
	}

	wcsdb := make([]model.WeeklyChampion, 0, len(wcs.FreeChampionIds))
	for _, wc := range wcs.FreeChampionIds {
		wcsdb = append(wcsdb, model.WeeklyChampion{
			ChampionID: strconv.Itoa(wc),
		})
	}

	return wcsdb, nil
}
