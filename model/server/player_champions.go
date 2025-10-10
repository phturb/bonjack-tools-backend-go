package server

type PlayerChampions []*PlayerChampion

type PlayerChampion struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Img   string `json:"img"`
	Owned bool   `json:"owned"`
}
