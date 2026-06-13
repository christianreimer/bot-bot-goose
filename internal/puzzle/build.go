// Package puzzle owns the shared puzzle composition logic — selecting prompts,
// picking approved bots and decoys for a round, and the daily mode rotation.
//
// Two callers consume this package:
//   - bbg-puzzle-build (the 12:00 UTC cron) builds tomorrow's puzzle.
//   - bbg-admin puzzle compose|schedule (operator/agent driven) builds any
//     puzzle on demand.
//
// Both go through the same code path so cron output and ad-hoc output match.
//
// v1 selection is uniform-random over approved content. Step 9 of the plan
// replaces the pickers with the slot E/P/B bandit (Thompson sampling) without
// changing this package's public API: the shape — 1 bot + 3 decoys for
// find_the_bot, 3 bots + 1 decoy for find_the_human — is the stable contract.
package puzzle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

// ContentRow is one (id, text) pair from the approved pool. Exposed so callers
// can log which IDs landed in a round.
type ContentRow struct {
	ID   uuid.UUID
	Text string
}

// PickMode implements the mode-rotation policy: roughly 5 find_the_bot per 1
// find_the_human. Deterministic on date so idempotent re-runs match.
func PickMode(date time.Time) db.Mode {
	// 6-day cycle: 0..4 = find_the_bot, 5 = find_the_human. Anti-streak
	// (never 3 consecutive find_the_human) falls out of the cadence.
	dayNum := date.Unix() / 86400
	if dayNum%6 == 5 {
		return db.ModeFindHuman
	}
	return db.ModeFindBot
}

// SelectPrompts picks N non-retired prompts uniformly at random. Replaced by
// an LRU/diversity-aware picker in step 9.
func SelectPrompts(ctx context.Context, d *db.DB, n int) ([]uuid.UUID, error) {
	rows, err := d.Query(ctx, `
		SELECT id FROM prompts
		 WHERE retired_at IS NULL
		 ORDER BY random()
		 LIMIT $1
	`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0, n)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PickApprovedBots returns up to n approved bot_candidates for the prompt.
func PickApprovedBots(ctx context.Context, d *db.DB, promptID uuid.UUID, n int) ([]ContentRow, error) {
	rows, err := d.Query(ctx, `
		SELECT id, text FROM bot_candidates
		 WHERE prompt_id = $1 AND status = 'approved'
		 ORDER BY random()
		 LIMIT $2
	`, promptID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ContentRow, 0, n)
	for rows.Next() {
		var c ContentRow
		if err := rows.Scan(&c.ID, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PickApprovedDecoys returns up to n approved (non-deleted) decoys for the prompt.
func PickApprovedDecoys(ctx context.Context, d *db.DB, promptID uuid.UUID, n int) ([]ContentRow, error) {
	rows, err := d.Query(ctx, `
		SELECT id, text FROM decoy_submissions
		 WHERE prompt_id = $1 AND status = 'approved' AND deleted_at IS NULL
		 ORDER BY random()
		 LIMIT $2
	`, promptID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ContentRow, 0, n)
	for rows.Next() {
		var c ContentRow
		if err := rows.Scan(&c.ID, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ErrInsufficientContent is returned when the approved pool can't fill a round.
var ErrInsufficientContent = errors.New("not enough approved content for round")

// ErrBadExplicitPick is returned by the explicit-pick path when an operator
// passed a bot/decoy id that doesn't exist, isn't approved, doesn't belong to
// the round's prompt, or duplicates another id in the same round. Carries a
// human-readable message naming the offending id.
type ErrBadExplicitPick struct{ Msg string }

func (e *ErrBadExplicitPick) Error() string { return e.Msg }

// PickBotsByIDs validates that every id in ids names an approved bot_candidate
// for the given prompt and returns the matching (id, text) rows in input order.
// Duplicates in `ids` are rejected — round answers must be distinct.
func PickBotsByIDs(ctx context.Context, d *db.DB, promptID uuid.UUID, ids []uuid.UUID) ([]ContentRow, error) {
	if dup := firstDuplicate(ids); dup != uuid.Nil {
		return nil, &ErrBadExplicitPick{Msg: fmt.Sprintf("bot id %s listed twice", dup)}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := d.Query(ctx, `
		SELECT id, text FROM bot_candidates
		 WHERE id = ANY($1::uuid[]) AND prompt_id = $2 AND status = 'approved'
	`, ids, promptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[uuid.UUID]ContentRow{}
	for rows.Next() {
		var c ContentRow
		if err := rows.Scan(&c.ID, &c.Text); err != nil {
			return nil, err
		}
		got[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ContentRow, 0, len(ids))
	for _, id := range ids {
		c, ok := got[id]
		if !ok {
			return nil, &ErrBadExplicitPick{Msg: fmt.Sprintf("bot %s not approved or not for prompt %s", id, promptID)}
		}
		out = append(out, c)
	}
	return out, nil
}

// PickDecoysByIDs validates that every id in ids names an approved, non-deleted
// decoy_submission for the given prompt and returns rows in input order.
func PickDecoysByIDs(ctx context.Context, d *db.DB, promptID uuid.UUID, ids []uuid.UUID) ([]ContentRow, error) {
	if dup := firstDuplicate(ids); dup != uuid.Nil {
		return nil, &ErrBadExplicitPick{Msg: fmt.Sprintf("decoy id %s listed twice", dup)}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := d.Query(ctx, `
		SELECT id, text FROM decoy_submissions
		 WHERE id = ANY($1::uuid[]) AND prompt_id = $2
		   AND status = 'approved' AND deleted_at IS NULL
	`, ids, promptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[uuid.UUID]ContentRow{}
	for rows.Next() {
		var c ContentRow
		if err := rows.Scan(&c.ID, &c.Text); err != nil {
			return nil, err
		}
		got[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ContentRow, 0, len(ids))
	for _, id := range ids {
		c, ok := got[id]
		if !ok {
			return nil, &ErrBadExplicitPick{Msg: fmt.Sprintf("decoy %s not approved or not for prompt %s", id, promptID)}
		}
		out = append(out, c)
	}
	return out, nil
}

// ComposeRoundAnswersExplicit is the deliberate counterpart of
// ComposeRoundAnswers: instead of random picks, the operator names the bot and
// decoy ids. Count enforcement mirrors the random path:
//
//	find_the_bot:   1 bot  + 3 decoys
//	find_the_human: 3 bots + 1 decoy
//
// Returns ErrBadExplicitPick with a human-readable message on bad ids,
// duplicates, or count mismatches.
func ComposeRoundAnswersExplicit(ctx context.Context, d *db.DB, promptID uuid.UUID, mode db.Mode, botIDs, decoyIDs []uuid.UUID) ([]db.Answer, error) {
	wantBots, wantDecoys := 1, 3
	if mode == db.ModeFindHuman {
		wantBots, wantDecoys = 3, 1
	}
	if len(botIDs) != wantBots {
		return nil, &ErrBadExplicitPick{Msg: fmt.Sprintf("mode=%s needs %d bot id(s), got %d", mode, wantBots, len(botIDs))}
	}
	if len(decoyIDs) != wantDecoys {
		return nil, &ErrBadExplicitPick{Msg: fmt.Sprintf("mode=%s needs %d decoy id(s), got %d", mode, wantDecoys, len(decoyIDs))}
	}
	bots, err := PickBotsByIDs(ctx, d, promptID, botIDs)
	if err != nil {
		return nil, err
	}
	decoys, err := PickDecoysByIDs(ctx, d, promptID, decoyIDs)
	if err != nil {
		return nil, err
	}
	out := make([]db.Answer, 0, wantBots+wantDecoys)
	for _, b := range bots {
		b := b
		out = append(out, db.Answer{ContentKind: db.ContentBot, BotCandidateID: &b.ID, AnswerText: b.Text})
	}
	for _, dc := range decoys {
		dc := dc
		out = append(out, db.Answer{ContentKind: db.ContentDecoy, DecoyID: &dc.ID, AnswerText: dc.Text})
	}
	return out, nil
}

// firstDuplicate returns the first uuid that appears more than once in ids, or
// uuid.Nil if all ids are distinct. Cheap O(n) check; the lists are at most 4.
func firstDuplicate(ids []uuid.UUID) uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return id
		}
		seen[id] = struct{}{}
	}
	return uuid.Nil
}

// ComposeRoundAnswers picks the 4 answers for one round, mode-aware.
//
// find_the_bot:   1 bot + 3 decoys (player hunts the bot)
// find_the_human: 3 bots + 1 decoy (player hunts the human)
//
// Returns ErrInsufficientContent if the approved pool can't fill the round.
func ComposeRoundAnswers(ctx context.Context, d *db.DB, promptID uuid.UUID, mode db.Mode) ([]db.Answer, error) {
	if mode == db.ModeFindHuman {
		bots, err := PickApprovedBots(ctx, d, promptID, 3)
		if err != nil {
			return nil, err
		}
		decoys, err := PickApprovedDecoys(ctx, d, promptID, 1)
		if err != nil {
			return nil, err
		}
		if len(bots) < 3 || len(decoys) < 1 {
			return nil, ErrInsufficientContent
		}
		out := make([]db.Answer, 0, 4)
		for _, b := range bots {
			b := b
			out = append(out, db.Answer{ContentKind: db.ContentBot, BotCandidateID: &b.ID, AnswerText: b.Text})
		}
		out = append(out, db.Answer{ContentKind: db.ContentDecoy, DecoyID: &decoys[0].ID, AnswerText: decoys[0].Text})
		return out, nil
	}
	// find_the_bot: 1 bot + 3 decoys.
	bots, err := PickApprovedBots(ctx, d, promptID, 1)
	if err != nil {
		return nil, err
	}
	decoys, err := PickApprovedDecoys(ctx, d, promptID, 3)
	if err != nil {
		return nil, err
	}
	if len(bots) < 1 || len(decoys) < 3 {
		return nil, ErrInsufficientContent
	}
	out := []db.Answer{
		{ContentKind: db.ContentBot, BotCandidateID: &bots[0].ID, AnswerText: bots[0].Text},
	}
	for _, dc := range decoys {
		dc := dc
		out = append(out, db.Answer{ContentKind: db.ContentDecoy, DecoyID: &dc.ID, AnswerText: dc.Text})
	}
	return out, nil
}
