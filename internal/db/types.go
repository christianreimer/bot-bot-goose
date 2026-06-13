package db

import (
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/game"
	"github.com/google/uuid"
)

// Re-export game's outcome type so callers can pass it through this package
// without an extra import. The DB layer is otherwise free of domain
// dependencies; this alias stays narrow.
type Outcome = game.Outcome

type ContentKind string

const (
	ContentBot   ContentKind = "bot"
	ContentDecoy ContentKind = "decoy"
)

type User struct {
	ID         uuid.UUID
	Handle     *string
	Email      *string
	Role       string
	SpotterELO float64
	CreatedAt  time.Time
}

// PublicHandle returns the string the leaderboard / share pages should
// show. Empty handle → "anonymous"; otherwise the handle itself. Single
// source of truth so we don't drift across surfaces.
func (u *User) PublicHandle() string {
	if u == nil {
		return "anonymous"
	}
	if u.Handle == nil || *u.Handle == "" {
		return "anonymous"
	}
	return *u.Handle
}

type DailyPuzzle struct {
	ID           uuid.UUID
	PuzzleNumber int32
	PuzzleDate   time.Time
	FrozenAt     time.Time
	Theme        *string
}

type PuzzleRound struct {
	ID            uuid.UUID
	DailyPuzzleID uuid.UUID
	RoundIndex    int16
	PromptID      uuid.UUID
	PromptText    string
	TargetCount   int16
}

// Answer is the canonical, unordered record. AnswerText is the snapshot used
// at play time; no slot index lives here.
type Answer struct {
	ID             uuid.UUID
	RoundID        uuid.UUID
	ContentKind    ContentKind
	BotCandidateID *uuid.UUID
	DecoyID        *uuid.UUID
	IsTrap         bool
	AuthorUserID   *uuid.UUID
	AnswerText     string
}

type Play struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	DailyPuzzleID uuid.UUID
	StartedAt     time.Time
	CompletedAt   *time.Time
	ScorePct      *int16
	HMACSecret    []byte
}

type PlayRound struct {
	ID              uuid.UUID
	PlayID          uuid.UUID
	RoundIndex      int16
	SlotPermutation []int16 // slot_permutation[i] = ordinal of canonical answer at client slot i
	HintUsed        bool
	RemovedSlot     *int16
	StartedAt       time.Time
	CommittedAt     *time.Time
}
