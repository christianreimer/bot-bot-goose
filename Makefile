# Bot Bot Goose — operator UI. Everything you'd put in a README runbook goes here.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

# ---- environment ------------------------------------------------------------

DB_URL ?= postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable
GOOSE_DIR := db/migrations
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@latest
SQLC  := go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest

COMPOSE := docker compose -f deploy/docker-compose.yml --env-file deploy/compose.env

# Cloudflare Quick Tunnel state. The local stack ships traffic through a
# free trycloudflare.com URL so the user can hit the app from a phone or
# share a link without router/firewall work. State lives in .tunnel/ —
# gitignored, regenerated every `make up`.
TUNNEL_DIR := .tunnel
TUNNEL_LOG := $(TUNNEL_DIR)/cloudflared.log
TUNNEL_PID := $(TUNNEL_DIR)/cloudflared.pid
TUNNEL_URL_REGEX := 'https://[a-z0-9-]+\.trycloudflare\.com'
# -a forces grep to treat the cloudflared log as text — its color-escape
# bytes sometimes trip grep's binary-file heuristic.
TUNNEL_GREP := grep -aoE $(TUNNEL_URL_REGEX)

# ---- docker-compose lifecycle ----------------------------------------------

.PHONY: up up-prod down rebuild logs logs-prod ps tunnel-up tunnel-down tunnel-url

# Local-testing stack: bbg + bundled postgres + bundled valkey, plus a
# Cloudflare Quick Tunnel so the local instance gets a public URL.
# Caddy stays out (it would just dead-end trying to provision certs for
# localhost — reserved for `up-prod`). The `local` profile opts postgres
# and valkey into the compose lifecycle.
up:
	$(COMPOSE) --profile local up -d
	@$(MAKE) --no-print-directory tunnel-up

# Production stack (DigitalOcean droplet): bbg + Caddy as the TLS edge.
# Postgres + Valkey are NOT bundled — the bbg container talks to DO
# Managed Postgres and DO Managed Caching via BBG_DB_URL / BBG_VALKEY_URL
# set in compose.env. See plans/launch-capacity.md §5.2 + §5.3.
up-prod:
	$(COMPOSE) --profile edge up -d

# Bring down everything regardless of which profile started it.
down:
	$(COMPOSE) --profile local --profile edge down
	@$(MAKE) --no-print-directory tunnel-down

# Start a Cloudflare Quick Tunnel pointed at the local bbg port and print
# the public URL once cloudflared has it. Idempotent: a re-run prints the
# existing tunnel's URL instead of spawning a second daemon.
#
# The body is one big shell block so the early-exit branches (already
# running / cloudflared missing) don't fall through to spawning a second
# daemon. Each Makefile recipe line otherwise runs in its own shell, so
# `exit 0` in one wouldn't bail out of the recipe.
tunnel-up:
	@set -e; \
	if ! command -v cloudflared >/dev/null 2>&1; then \
		echo "cloudflared not installed — skipping tunnel"; \
		echo "  install: brew install cloudflared"; \
		exit 0; \
	fi; \
	mkdir -p $(TUNNEL_DIR); \
	if [ -f $(TUNNEL_PID) ] && kill -0 $$(cat $(TUNNEL_PID)) 2>/dev/null; then \
		url=$$($(TUNNEL_GREP) $(TUNNEL_LOG) 2>/dev/null | head -1); \
		echo "tunnel already running (pid $$(cat $(TUNNEL_PID)))"; \
		[ -n "$$url" ] && echo "tunnel URL: $$url" || echo "tunnel URL: <not yet logged>"; \
		exit 0; \
	fi; \
	: > $(TUNNEL_LOG); \
	nohup cloudflared tunnel --url http://localhost:8080 > $(TUNNEL_LOG) 2>&1 & echo $$! > $(TUNNEL_PID); \
	printf "starting cloudflare tunnel"; \
	for i in $$(seq 1 30); do \
		url=$$($(TUNNEL_GREP) $(TUNNEL_LOG) 2>/dev/null | head -1); \
		if [ -n "$$url" ]; then \
			echo ""; echo ""; \
			echo "  bbg: http://localhost:8080"; \
			echo "  tunnel URL: $$url"; \
			echo ""; \
			echo "  logs: tail -f $(TUNNEL_LOG)"; \
			echo "  stop: make tunnel-down (or make down)"; \
			exit 0; \
		fi; \
		printf "."; sleep 1; \
	done; \
	echo " timed out after 30s"; \
	echo "see $(TUNNEL_LOG)"; \
	exit 1

# Stop the running tunnel. No-op when nothing is running.
tunnel-down:
	@if [ -f $(TUNNEL_PID) ]; then \
		pid=$$(cat $(TUNNEL_PID)); \
		if kill -0 $$pid 2>/dev/null; then \
			echo "stopping cloudflare tunnel (pid $$pid)"; \
			kill $$pid 2>/dev/null || true; \
		fi; \
		rm -f $(TUNNEL_PID); \
	fi

# Print the current tunnel URL. Useful when re-attaching to a long-lived
# `make up` from a fresh terminal.
tunnel-url:
	@if [ ! -f $(TUNNEL_PID) ] || ! kill -0 $$(cat $(TUNNEL_PID) 2>/dev/null) 2>/dev/null; then \
		echo "no tunnel running" >&2; exit 1; \
	fi; \
	url=$$($(TUNNEL_GREP) $(TUNNEL_LOG) 2>/dev/null | head -1); \
	if [ -z "$$url" ]; then echo "tunnel URL not yet visible in log" >&2; exit 1; fi; \
	echo "$$url"

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
	go run ./cmd/puzzle-build $${DATE_ARG:+--date=$$DATE_ARG}

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
