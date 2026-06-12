package db

import (
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/game"
	"github.com/google/uuid"
)

// Re-export game's value types so callers can pass them through this package
// without an extra import. The DB layer is otherwise free of domain
// dependencies; these aliases stay narrow.
type (
	Mode    = game.Mode
	Outcome = game.Outcome
)

const (
	ModeFindBot   = game.FindTheBot
	ModeFindHuman = game.FindTheHuman
)

type ContentKind string

const (
	ContentBot   ContentKind = "bot"
	ContentDecoy ContentKind = "decoy"
)

type User struct {
	ID               uuid.UUID
	Handle           *string
	Email            *string
	Role             string
	SpotterELO       float64
	DisplayAnonymous bool
	CreatedAt        time.Time
}

// PublicHandle returns the string the leaderboard / share pages should
// show. Empty handle or DisplayAnonymous=true → "anonymous". This is the
// single source of truth for the privacy toggle so we don't drift across
// surfaces.
func (u *User) PublicHandle() string {
	if u == nil || u.DisplayAnonymous {
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
	Mode         Mode
	FrozenAt     time.Time
	Theme        *string
}

type PuzzleRound struct {
	ID            uuid.UUID
	DailyPuzzleID uuid.UUID
	RoundIndex    int16
	PromptID      uuid.UUID
	PromptText    string
	TargetKind    string // 'bot' or 'human' — what the player hunts
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
