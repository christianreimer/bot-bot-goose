// bbg — the main Bot Bot Goose HTTP server. Stays minimal on purpose: this
// binary's only job is wire dependencies, run the migrations if asked, and
// hand control to internal/httpx.
package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/christianreimer/bot-bot-goose/db/migrations"
	"github.com/christianreimer/bot-bot-goose/internal/cache"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/email"
	"github.com/christianreimer/bot-bot-goose/internal/httpx"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	listen := envDefault("BBG_LISTEN", ":8080")
	// Optional second listener for /metrics. Bind to a loopback / private IP
	// so Caddy + Cloudflare don't proxy it. Empty disables metrics serving
	// (the counters are still maintained — they just aren't exposed via HTTP).
	metricsListen := os.Getenv("BBG_METRICS_LISTEN")
	dbURL := envDefault("BBG_DB_URL", "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable")
	baseURL := envDefault("BBG_BASE_URL", "http://localhost:8080")
	webDir := envDefault("BBG_WEB_DIR", "web")
	dev := os.Getenv("BBG_DEV") != ""

	sessionKeyHex := os.Getenv("BBG_SESSION_KEY")
	if sessionKeyHex == "" {
		if dev {
			// Deterministic dev key so cookies survive restarts. Never used in prod.
			sessionKeyHex = strings.Repeat("ab", 32)
			log.Warn("using deterministic dev session key — never run this in prod")
		} else {
			log.Error("BBG_SESSION_KEY required (hex, 32+ bytes)")
			os.Exit(1)
		}
	}
	sessionKey, err := hex.DecodeString(sessionKeyHex)
	if err != nil || len(sessionKey) < 32 {
		log.Error("BBG_SESSION_KEY must be ≥32 hex-encoded bytes", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Apply embedded migrations before opening the connection pool, unless
	// BBG_SKIP_MIGRATIONS=1. Idempotent: goose's up command is a fast no-op
	// when the DB is already at the latest version. This is the deploy
	// hook on DO App / DO Droplet / anywhere we don't run a separate
	// goose binary — every bbg boot self-heals to the schema in
	// db/migrations/.
	if os.Getenv("BBG_SKIP_MIGRATIONS") == "" {
		if err := applyMigrations(ctx, dbURL, log); err != nil {
			log.Error("apply migrations", "err", err)
			os.Exit(1)
		}
	}

	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		log.Error("open db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Optional Valkey cache. When BBG_VALKEY_URL is empty (the dev default)
	// every cache layer falls back to direct Postgres — see plans/launch-
	// capacity §2. The construction blocks on a Ping so a typo'd URL fails
	// at boot instead of at the first read.
	valkeyURL := os.Getenv("BBG_VALKEY_URL")
	cacheClient, err := cache.New(ctx, valkeyURL, log)
	if err != nil {
		log.Error("open valkey", "err", err)
		os.Exit(1)
	}
	defer cacheClient.Close()
	if cacheClient.Enabled() {
		log.Info("valkey enabled")
	}

	// Email sender. BBG_EMAIL_PROVIDER controls which one runs; "log" is
	// the safe dev default that prints magic links to stdout. Production
	// flips to "resend" and requires BBG_RESEND_API_KEY + BBG_EMAIL_FROM.
	var sender email.Sender
	switch os.Getenv("BBG_EMAIL_PROVIDER") {
	case "resend":
		sender = &email.ResendSender{
			APIKey: os.Getenv("BBG_RESEND_API_KEY"),
			From:   os.Getenv("BBG_EMAIL_FROM"),
		}
	default:
		sender = &email.LogSender{Log: log}
		if !dev {
			log.Warn("BBG_EMAIL_PROVIDER not set — magic links will be logged, not sent")
		}
	}
	if err := email.AssertConfigured(sender); err != nil {
		log.Error("email sender misconfigured", "err", err)
		os.Exit(1)
	}

	srv, err := httpx.New(httpx.Config{
		Listen:        listen,
		MetricsListen: metricsListen,
		BaseURL:       baseURL,
		WebDir:        webDir,
		SessionKey:    sessionKey,
		SecureCookie:  !dev, // local http needs Secure=false
		DB:            pool,
		Cache:         cacheClient,
		Email:         sender,
		Logger:        log,
		// BBG_PRELAUNCH_MODE swaps the front page (and every URL that isn't
		// /prelaunch, /privacy, or system routes) for an on-brand "coming
		// soon" placeholder. Flip off at launch.
		PrelaunchMode: os.Getenv("BBG_PRELAUNCH_MODE") != "",
	})
	if err != nil {
		log.Error("build server", "err", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info("shutdown signal received")
		cancel()
	}()

	if err := srv.ListenAndServe(ctx); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// applyMigrations opens a short-lived database/sql connection (goose's
// dialect interface needs *sql.DB, not pgxpool), runs the migrations
// embedded in db/migrations/, and closes. The actual application pool
// is opened separately right after this returns.
func applyMigrations(ctx context.Context, dbURL string, log *slog.Logger) error {
	sqlDB, err := sql.Open("pgx", dbURL)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	before, _ := goose.GetDBVersion(sqlDB)
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return err
	}
	after, _ := goose.GetDBVersion(sqlDB)
	if after != before {
		log.Info("migrations applied", "from", before, "to", after)
	} else {
		log.Info("migrations up-to-date", "version", after)
	}
	return nil
}
