# Contributing

Thanks for your interest in Bot Bot Goose. This file's intentionally short — the codebase is small enough that the most useful onboarding is reading [`README.md`](README.md) and then `internal/play/` (the integrity backbone) end-to-end.

## Reporting bugs

Open a [GitHub Issue](https://github.com/christianreimer/bot-bot-goose/issues/new) with:

- What you did, what you expected, what happened.
- Server log lines if relevant (`make logs`).
- Whether you can reproduce it from a fresh `make up && make migrate && make seed`.

If the bug is a **security issue** — anything that lets a player see other players' labels, replay a token, or bypass the integrity rules listed in the README — do **not** open a public issue. Use [`SECURITY.md`](SECURITY.md).

## Submitting changes

1. Fork the repo, branch from `main` (`feature/...` or `fix/...` naming is fine).
2. Make your change. Keep the diff focused — bundling unrelated cleanups makes review hard.
3. Run the checks:

   ```bash
   go vet ./...
   go test ./...
   ```

4. Open a PR with a description that covers the *why* as well as the *what*. If it touches the integrity backbone (`internal/play/`, the guess/hint/start handlers, slot permutation, HMAC tokens) call that out explicitly — those areas are reviewed more carefully and need tests in the same PR.

The repo follows standard Go style:

- `gofmt`'d code (any editor does this automatically).
- Lower-case package names matching their directory.
- Test files live next to the code they cover; `_test.go` suffix.
- No comments restating what the code does — comments are for *why* (a subtle invariant, an explanation that wouldn't survive a rename).

## Running the test suite

```bash
go test ./...
```

There are no integration tests against a real Postgres yet — every test in this repo is hermetic and runs in under a second. If you add tests that need the database, plumb them through `docker compose -f deploy/docker-compose.yml up postgres` in a `// +build integration` file so `go test ./...` stays fast for everyone else.

## Working on the integrity backbone

`internal/play/` is the load-bearing anti-cheat code. The non-negotiable invariants:

- Answer labels never appear in any response except the guess-commit reveal.
- Every state-changing call carries an HMAC'd play token. Token verification must check play-owner, perm-hash, round index, and issued-at expiry.
- Slot permutations are generated server-side with `crypto/rand`. The canonical order of `puzzle_round_answers` rows (by UUID) is what the permutation indexes into; the canonical order is the *only* order that ever leaves the server in label form.

If a PR weakens any of these, please explain in the PR body why the change is safe and add a regression test that would have caught the original mistake.

## Working on content (decoys, prompts, bots)

Content lives in two places:

- **Hand-authored, version-controlled**: JSON files under `content/`. See `content/sample-2026-06.json` for the schema. Use `bbg-admin import` to apply.
- **Player-submitted**: `decoy_submissions` table. The route is `POST /api/decoy/submit`; reviewer approval happens in the moderation queue (currently a SQL update; UI is TODO).

The pattern is **solicit, don't scrape** (design doc §3). Don't open a PR that ingests human content from a third-party platform — it's both a legal hazard and a contamination risk for the core mechanic.

## What we won't merge (without good reason)

- A switch to a JS framework on the front-end. The "no build step" property is load-bearing for the project's vibe and ops simplicity.
- New required dependencies for the live request path. The Dockerfile output is a single static Go binary; keeping it that way is on purpose.
- Calls to an LLM on the live request path. LLM generation is offline (`cmd/bot-candidates`) by design — see the plan and design doc §7.
- Features behind feature flags. If a change isn't ready, it lives on a branch, not behind a runtime toggle.

## Code of conduct

Be kind, be brief, assume good faith. If a discussion gets heated, the maintainer reserves the right to lock it.
