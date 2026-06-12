// Package content holds the static archetype roster from design doc §5.
//
// Archetypes are intentionally hand-curated and seeded into the DB so the
// composer can weight them. New archetypes go through a migration + a
// `make seed` re-run rather than a hot-config edit, because the marketing
// beat ("a new goose has entered the pond") is tied to release cadence.
package content

type Archetype struct {
	Slug       string
	Name       string
	Tell       string
	Difficulty int16
}

// StarterRoster is the easy → hard ordering from the design doc table.
// The Mirror is intentionally last and intentionally "advanced" — it's
// the boss tier trained on the human pool.
var StarterRoster = []Archetype{
	{Slug: "hedger", Name: "The Hedger", Tell: "Qualifies everything, commits to nothing.", Difficulty: 1},
	{Slug: "sunbeam", Name: "The Sunbeam", Tell: "Aggressively wholesome, gratitude, sets a positive tone.", Difficulty: 1},
	{Slug: "lecturer", Name: "The Lecturer", Tell: "Turns everything into a lesson.", Difficulty: 2},
	{Slug: "lister", Name: "The Lister", Tell: "Over-structures casual answers into tidy tricolons.", Difficulty: 2},
	{Slug: "dodger", Name: "The Dodger", Tell: "Refuses to actually answer.", Difficulty: 2},
	{Slug: "romantic", Name: "The Romantic", Tell: "Flowery, vivid-but-generic imagery.", Difficulty: 3},
	{Slug: "over-corrector", Name: "The Over-Corrector", Tell: "Studied informality, but the casualness is too even.", Difficulty: 4},
	{Slug: "mirror", Name: "The Mirror", Tell: "Trained on the human pool. Few tells.", Difficulty: 5},
}
