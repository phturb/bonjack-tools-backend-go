// Package swiss implements Swiss-system tournaments: configuration, participants,
// server-side pairing with rematch avoidance and byes, round lifecycle and the
// REST API consumed by the website.
//
// Wins, losses, Buchholz, participant status and standings are never stored;
// they are derived from the match rows on every read. Only the configuration,
// the participants (name, seed, dropped flag) and the matches are persisted.
package swiss

import (
	"crypto/rand"
	"math/big"
	"time"
)

// Tournament statuses. "completed" means the Swiss stage is over; the owner
// may then start single-elimination playoffs among the advanced players.
const (
	StatusDraft      = "draft"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusPlayoffs   = "playoffs"
	StatusFinished   = "finished"
)

// Match stages. The empty string is the Swiss stage (rows created before
// playoffs existed have it empty).
const (
	StageSwiss    = ""
	StagePlayoffs = "playoffs"
)

// Seeding strategies.
const (
	SeedingManual = "manual"
	SeedingRandom = "random"
)

// Visibilities.
const (
	VisibilityPublic   = "public"
	VisibilityUnlisted = "unlisted"
)

// Participant statuses, computed on read.
const (
	ParticipantActive     = "active"
	ParticipantAdvanced   = "advanced"
	ParticipantEliminated = "eliminated"
	ParticipantDropped    = "dropped"
)

// Tournament is the persisted tournament row.
type Tournament struct {
	ID                string `gorm:"primaryKey;size:16"`
	Name              string `gorm:"size:80"`
	Description       string `gorm:"size:500"`
	Status            string `gorm:"size:16;index"`
	OwnerSub          string `gorm:"size:255;index"`
	RNGSeed           int64
	WinsToAdvance     int
	LossesToEliminate int
	BestOf            int
	DecidersBestOf    int
	PlayoffsBestOf    int
	Seeding           string `gorm:"size:16"`
	AllowRematches    bool
	Visibility        string `gorm:"size:16"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TableName sets the table name.
func (Tournament) TableName() string { return "swiss_tournaments" }

// MaxRounds is winsToAdvance + lossesToEliminate - 1.
func (t Tournament) MaxRounds() int { return t.WinsToAdvance + t.LossesToEliminate - 1 }

// Participant is the persisted participant row. Dropped is the only status
// information stored; advanced and eliminated are derived from matches.
type Participant struct {
	ID           string      `gorm:"primaryKey;size:16"`
	TournamentID string      `gorm:"size:16;index;not null"`
	Tournament   *Tournament `gorm:"foreignKey:TournamentID;constraint:OnDelete:CASCADE"`
	Name         string      `gorm:"size:80"`
	Seed         int
	Dropped      bool
}

// TableName sets the table name.
func (Participant) TableName() string { return "swiss_participants" }

// Match is the persisted match row. A bye has P2ID nil and is auto-won by P1.
type Match struct {
	ID           string      `gorm:"primaryKey;size:16"`
	TournamentID string      `gorm:"size:16;index;not null"`
	Tournament   *Tournament `gorm:"foreignKey:TournamentID;constraint:OnDelete:CASCADE"`
	Stage        string      `gorm:"size:16;index"`
	Round        int         `gorm:"index"`
	BucketWins   int
	BucketLosses int
	P1ID         string  `gorm:"size:16;not null"`
	P2ID         *string `gorm:"size:16"`
	BestOf       int
	P1Score      int
	P2Score      int
	WinnerID     *string `gorm:"size:16"`
	Bye          bool
	Rematch      bool
	Forfeit      bool
	CreatedAt    time.Time
}

// TableName sets the table name.
func (Match) TableName() string { return "swiss_matches" }

// Resolved reports whether the match has a winner.
func (m Match) Resolved() bool { return m.WinnerID != nil && *m.WinnerID != "" }

// Models returns the GORM models of the package, for AutoMigrate.
func Models() []any {
	return []any{&Tournament{}, &Participant{}, &Match{}}
}

// ---- JSON DTOs -------------------------------------------------------------

// ConfigInput is the body of POST /api/swiss and PATCH /api/swiss/{id}.
// Every field is optional so the same struct serves partial updates.
type ConfigInput struct {
	Name              *string `json:"name"`
	Description       *string `json:"description"`
	WinsToAdvance     *int    `json:"winsToAdvance"`
	LossesToEliminate *int    `json:"lossesToEliminate"`
	BestOf            *int    `json:"bestOf"`
	DecidersBestOf    *int    `json:"decidersBestOf"`
	PlayoffsBestOf    *int    `json:"playoffsBestOf"`
	Seeding           *string `json:"seeding"`
	AllowRematches    *bool   `json:"allowRematches"`
	Visibility        *string `json:"visibility"`
}

// ParticipantInput is one entry of PUT /api/swiss/{id}/participants.
type ParticipantInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Seed int    `json:"seed"`
}

// ResultInput is the body of PUT /api/swiss/{id}/matches/{mid}/result.
type ResultInput struct {
	P1Score int `json:"p1Score"`
	P2Score int `json:"p2Score"`
}

// ConfigDTO is the config object of TournamentDTO.
type ConfigDTO struct {
	WinsToAdvance     int    `json:"winsToAdvance"`
	LossesToEliminate int    `json:"lossesToEliminate"`
	BestOf            int    `json:"bestOf"`
	DecidersBestOf    int    `json:"decidersBestOf"`
	PlayoffsBestOf    int    `json:"playoffsBestOf"`
	Seeding           string `json:"seeding"`
	AllowRematches    bool   `json:"allowRematches"`
	Visibility        string `json:"visibility"`
}

// ParticipantDTO is a participant with its derived record.
type ParticipantDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Seed     int    `json:"seed"`
	Wins     int    `json:"wins"`
	Losses   int    `json:"losses"`
	Buchholz int    `json:"buchholz"`
	Status   string `json:"status"`
}

// BucketDTO is the record shared by both participants of a match.
type BucketDTO struct {
	Wins   int `json:"wins"`
	Losses int `json:"losses"`
}

// MatchDTO is a match.
type MatchDTO struct {
	ID      string    `json:"id"`
	Bucket  BucketDTO `json:"bucket"`
	BestOf  int       `json:"bestOf"`
	P1      string    `json:"p1"`
	P2      *string   `json:"p2"`
	P1Score int       `json:"p1Score"`
	P2Score int       `json:"p2Score"`
	Winner  *string   `json:"winner"`
	Bye     bool      `json:"bye"`
	Rematch bool      `json:"rematch"`
	Forfeit bool      `json:"forfeit"`
}

// RoundDTO is a round.
type RoundDTO struct {
	Number  int        `json:"number"`
	Closed  bool       `json:"closed"`
	Matches []MatchDTO `json:"matches"`
}

// PlayoffsDTO is the single-elimination stage, present once playoffs started.
// Size is the bracket size (power of two); Rounds[len-1] is the final once
// generated. Champion is set when the status is finished.
type PlayoffsDTO struct {
	BestOf       int        `json:"bestOf"`
	Size         int        `json:"size"`
	CurrentRound int        `json:"currentRound"`
	Rounds       []RoundDTO `json:"rounds"`
	Champion     *string    `json:"champion"`
}

// TournamentDTO is the full tournament, response of every endpoint but the list.
type TournamentDTO struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Status       string           `json:"status"`
	Config       ConfigDTO        `json:"config"`
	OwnerSub     string           `json:"ownerSub"`
	CreatedAt    time.Time        `json:"createdAt"`
	CurrentRound int              `json:"currentRound"`
	Participants []ParticipantDTO `json:"participants"`
	Rounds       []RoundDTO       `json:"rounds"`
	Standings    []string         `json:"standings"`
	Playoffs     *PlayoffsDTO     `json:"playoffs"`
}

// TournamentSummary is one entry of GET /api/swiss.
// Participants is the participant count.
type TournamentSummary struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Participants int       `json:"participants"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ---- IDs -------------------------------------------------------------------

const idAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
const idLength = 10

// NewID returns a 10-character base62 identifier from crypto/rand.
func NewID() (string, error) {
	b := make([]byte, idLength)
	max := big.NewInt(int64(len(idAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = idAlphabet[n.Int64()]
	}
	return string(b), nil
}

// NewSeed returns a random 63-bit seed from crypto/rand.
func NewSeed() (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return 0, err
	}
	return n.Int64(), nil
}
