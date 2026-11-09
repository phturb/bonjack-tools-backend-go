package swiss

import (
	"context"
	"net/http"

	"gorm.io/gorm"
)

// Linking participants to site users (the players table, keyed by Discord id).
//
// Two ways: the owner assigns an existing player in the participants editor
// (ReplaceParticipants, playerId per entry), or a logged-in user claims a seat
// on the tournament page (Claim). Claiming creates the players row when the
// user has never been seen by the Discord bot, so people outside the voice
// channel can still be linked.

// Players lists every known site user for the editor's autocomplete.
func (s *Service) Players(ctx context.Context) ([]PlayerDTO, error) {
	var rows []playerRow
	if err := s.deps.Database(ctx).Order("name ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]PlayerDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, PlayerDTO{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

// withTournament runs fn inside a transaction after loading the tournament,
// without the ownership check (for actions any logged-in user may take).
func (s *Service) withTournament(ctx context.Context, id string, fn func(tx *gorm.DB, l *loaded) error) (*TournamentDTO, error) {
	var dto *TournamentDTO
	err := s.deps.Database(ctx).Transaction(func(tx *gorm.DB) error {
		l, err := fetch(tx, id)
		if err != nil {
			return err
		}
		if err := fn(tx, l); err != nil {
			return err
		}
		dto, err = s.load(tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return dto, nil
}

func findParticipant(l *loaded, pid string) *Participant {
	for i := range l.participants {
		if l.participants[i].ID == pid {
			return &l.participants[i]
		}
	}
	return nil
}

// Claim links the seat pid to the caller (playerID). Idempotent for the same
// caller; refused when the seat belongs to someone else or when the caller
// already holds another seat. Allowed in every status, including finished,
// so results can be attributed after the fact.
func (s *Service) Claim(ctx context.Context, subject, playerID, id, pid string) (*TournamentDTO, error) {
	return s.withTournament(ctx, id, func(tx *gorm.DB, l *loaded) error {
		p := findParticipant(l, pid)
		if p == nil {
			return &Error{http.StatusNotFound, "participant not found"}
		}
		if p.PlayerID != nil && *p.PlayerID == playerID {
			return nil
		}
		if p.PlayerID != nil && *p.PlayerID != "" {
			return errConflict("this seat is already linked to another user")
		}
		for _, other := range l.participants {
			if other.PlayerID != nil && *other.PlayerID == playerID {
				return errConflict("you already hold another seat in this tournament")
			}
		}
		// First time this user is seen: create the players row (name unknown
		// until the Discord bot sees them in the voice channel).
		var count int64
		if err := tx.Model(&playerRow{}).Where("id = ?", playerID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := tx.Create(&playerRow{ID: playerID}).Error; err != nil {
				return err
			}
		}
		s.log.Info("seat claimed", "tournament_id", id, "participant_id", pid, "subject", subject)
		return tx.Model(&Participant{}).Where("id = ?", pid).Update("player_id", playerID).Error
	})
}

// Unclaim removes the link of seat pid. Allowed to the tournament owner and
// to the linked user themselves, in every status.
func (s *Service) Unclaim(ctx context.Context, subject, playerID, id, pid string) (*TournamentDTO, error) {
	return s.withTournament(ctx, id, func(tx *gorm.DB, l *loaded) error {
		p := findParticipant(l, pid)
		if p == nil {
			return &Error{http.StatusNotFound, "participant not found"}
		}
		if p.PlayerID == nil || *p.PlayerID == "" {
			return nil
		}
		self := playerID != "" && *p.PlayerID == playerID
		if l.t.OwnerSub != subject && !self {
			s.log.Warn("unclaim refused", "tournament_id", id, "participant_id", pid, "subject", subject)
			return &Error{http.StatusForbidden, "only the creator or the linked user can unlink this seat"}
		}
		s.log.Info("seat unlinked", "tournament_id", id, "participant_id", pid, "subject", subject)
		return tx.Model(&Participant{}).Where("id = ?", pid).Update("player_id", nil).Error
	})
}
