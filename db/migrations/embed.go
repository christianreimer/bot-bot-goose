// Package migrations exposes the bbg SQL schema as an embedded fs.FS so the
// server binary can apply migrations at boot — no separate goose binary, no
// pre-deploy Job to wire up on DO App / DO Droplet / etc. The embed pattern
// keeps the migration files as the single source of truth: `make migrate`
// against a dev DB reads the same files goose.UpContext does in prod.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
