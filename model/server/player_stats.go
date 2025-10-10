package server

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
