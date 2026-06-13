# Bot Bot Goose — operator UI. Everything you'd put in a README runbook goes here.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

# ---- environment ------------------------------------------------------------

DB_URL ?= postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable
GOOSE_DIR := db/migrations
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@latest
SQLC  := go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest

COMPOSE := docker compose -f deploy/docker-compose.yml --env-file deploy/compose.env

# ---- docker-compose lifecycle ----------------------------------------------

.PHONY: up up-prod down rebuild logs logs-prod ps

# Local-testing stack: bbg + postgres only. Traffic comes in via the
# Cloudflare Tunnel daemon pointed straight at 127.0.0.1:8080 (Caddy
# would just be a dead branch trying to provision certs).
up:
	$(COMPOSE) up -d

# Production stack (DigitalOcean droplet): adds Caddy as the TLS edge.
# `--profile edge` opts the caddy service into the lifecycle.
up-prod:
	$(COMPOSE) --profile edge up -d

# `--profile edge` makes sure any running caddy container is stopped too.
# Without the flag, profile-gated services would be left up.
down:
	$(COMPOSE) --profile edge down

# Rebuilds the bbg image and recreates only its container; works under
# either `up` mode because the bbg service is profile-less.
rebuild:
	$(COMPOSE) build --no-cache bbg
	$(COMPOSE) up -d --force-recreate bbg

logs:
	$(COMPOSE) logs -f --tail=200 bbg

logs-prod:
	$(COMPOSE) logs -f --tail=200 bbg caddy

ps:
	$(COMPOSE) ps

# ---- db ---------------------------------------------------------------------

.PHONY: psql migrate migrate-down new-migration sqlc seed

psql:
	$(COMPOSE) exec postgres psql -U bbg -d bbg

migrate:
	$(GOOSE) -dir $(GOOSE_DIR) postgres "$(DB_URL)" up

migrate-down:
	$(GOOSE) -dir $(GOOSE_DIR) postgres "$(DB_URL)" down

new-migration:
	@if [ -z "$(name)" ]; then echo "usage: make new-migration name=add_foo"; exit 1; fi
	$(GOOSE) -dir $(GOOSE_DIR) create $(name) sql

sqlc:
	$(SQLC) generate

seed:
	go run ./cmd/admin seed

# ---- build / dev / test ----------------------------------------------------

.PHONY: dev test build ci lint

dev:
	$(MAKE) migrate
	BBG_DEV=1 go run ./cmd/server

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/bbg ./cmd/server
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/bbg-puzzle-build ./cmd/puzzle-build
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/bbg-bot-candidates ./cmd/bot-candidates
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/bbg-og-render ./cmd/og-render
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/bbg-admin ./cmd/admin

lint:
	go vet ./...

ci: lint test build

# ---- cron commands ---------------------------------------------------------

.PHONY: build-daily bot-gen og-render

build-daily:
	@DATE_ARG=$${DATE:-}; \
	MODE_ARG=$${MODE:-}; \
	go run ./cmd/puzzle-build $${DATE_ARG:+--date=$$DATE_ARG} $${MODE_ARG:+--mode=$$MODE_ARG}

bot-gen:
	@if [ -z "$(PROMPT)" ]; then echo "usage: make bot-gen PROMPT=\"...\" N=12"; exit 1; fi
	go run ./cmd/bot-candidates --prompt="$(PROMPT)" --n=$${N:-12}

og-render:
	@if [ -z "$(PUZZLE)" ]; then echo "usage: make og-render PUZZLE=142"; exit 1; fi
	go run ./cmd/og-render --puzzle=$(PUZZLE)

rollup:
	go run ./cmd/admin rollup

# ---- ops --------------------------------------------------------------------

.PHONY: backup restore admin-promote

backup:
	@mkdir -p backups
	@ts=$$(date -u +%Y%m%dT%H%M%SZ); \
	$(COMPOSE) exec -T postgres pg_dump -U bbg bbg | gzip > backups/$$ts.sql.gz; \
	echo "wrote backups/$$ts.sql.gz"

restore:
	@if [ -z "$(FILE)" ]; then echo "usage: make restore FILE=backups/...sql.gz"; exit 1; fi
	gunzip -c $(FILE) | $(COMPOSE) exec -T postgres psql -U bbg -d bbg

admin-promote:
	@if [ -z "$(EMAIL)" ] || [ -z "$(ROLE)" ]; then echo "usage: make admin-promote EMAIL=you@example.com ROLE=reviewer"; exit 1; fi
	go run ./cmd/admin promote --email="$(EMAIL)" --role="$(ROLE)"
