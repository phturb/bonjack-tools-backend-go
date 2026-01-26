package model

type PlayerChampions []*PlayerChampion

type PlayerChampion struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Img   string `json:"img"`
	Owned bool   `json:"owned"`
}

type UpdatePlayerChampionPayload struct {
	ID    string `json:"id"`
	Owned bool   `json:"owned"`
}
