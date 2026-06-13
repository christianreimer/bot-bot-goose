package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/christianreimer/bot-bot-goose/internal/db"
)

// dbFlags holds the values from the shared --db* flag set. Every subcommand
// registers these via registerDBFlags(fs) and then calls resolveDBURL() to
// get the final connection string.
//
// Resolution order (first match wins):
//  1. --db flag (used verbatim).
//  2. BBG_DB_URL env (used verbatim).
//  3. Assembled from --db-host/--db-port/--db-name/--db-user + password from
//     --db-password-env (or --db-password-file) + ssl params.
//
// The password is NEVER passed as a CLI flag — it comes from an env var or
// file so it doesn't leak into shell history or process listings.
type dbFlags struct {
	url          *string
	host         *string
	port         *string
	name         *string
	user         *string
	passwordEnv  *string
	passwordFile *string
	sslMode      *string
	sslRootCert  *string
}

func registerDBFlags(fs *flag.FlagSet) *dbFlags {
	return &dbFlags{
		url:          fs.String("db", os.Getenv("BBG_DB_URL"), "full Postgres DSN; overrides --db-* parts"),
		host:         fs.String("db-host", os.Getenv("BBG_DB_HOST"), "Postgres host (e.g., db-...ondigitalocean.com)"),
		port:         fs.String("db-port", envOr("BBG_DB_PORT", "5432"), "Postgres port (DigitalOcean managed: 25060)"),
		name:         fs.String("db-name", envOr("BBG_DB_NAME", "bbg"), "database name"),
		user:         fs.String("db-user", envOr("BBG_DB_USER", "bbg"), "database user"),
		passwordEnv:  fs.String("db-password-env", "BBG_DB_PASSWORD", "name of env var holding the password"),
		passwordFile: fs.String("db-password-file", os.Getenv("BBG_DB_PASSWORD_FILE"), "path to file holding the password"),
		sslMode:      fs.String("db-sslmode", "", "disable|require|verify-ca|verify-full (default: require for remote hosts, disable for localhost)"),
		sslRootCert:  fs.String("db-sslrootcert", os.Getenv("BBG_DB_SSLROOTCERT"), "path to CA bundle (required for verify-ca/verify-full)"),
	}
}

// resolveDBURL builds the final connection string. Returns the URL and a flag
// indicating whether the password was assembled (so the caller can warn if
// neither --db nor a password source was provided for a remote host).
func (f *dbFlags) resolveDBURL() (string, error) {
	if *f.url != "" {
		return *f.url, nil
	}
	if envURL := os.Getenv("BBG_DB_URL"); envURL != "" {
		return envURL, nil
	}
	if *f.host == "" {
		// fall back to the legacy default so local `make dev` keeps working
		// without any flags
		return "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable", nil
	}

	password, err := readPassword(*f.passwordEnv, *f.passwordFile)
	if err != nil {
		return "", err
	}

	sslMode := *f.sslMode
	if sslMode == "" {
		if isLocalHost(*f.host) {
			sslMode = "disable"
		} else {
			sslMode = "require"
		}
	}

	u := &url.URL{
		Scheme: "postgres",
		Host:   *f.host + ":" + *f.port,
		Path:   "/" + *f.name,
	}
	if password != "" {
		u.User = url.UserPassword(*f.user, password)
	} else {
		u.User = url.User(*f.user)
	}
	q := u.Query()
	q.Set("sslmode", sslMode)
	if *f.sslRootCert != "" {
		q.Set("sslrootcert", *f.sslRootCert)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// connectionSummary returns a host/db/sslmode summary safe to log. Never
// includes the password.
func (f *dbFlags) connectionSummary(resolved string) map[string]any {
	host, db, sslmode := parseSummary(resolved)
	return map[string]any{"host": host, "db": db, "sslmode": sslmode}
}

func readPassword(envVar, file string) (string, error) {
	if envVar != "" {
		if v := os.Getenv(envVar); v != "" {
			return v, nil
		}
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return "", nil
}

func isLocalHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// openDB resolves the URL from flags, opens the pool, and logs a redacted
// summary. Returns the error envelope through emitError on failure so callers
// just return its return value.
func openDB(ctx context.Context, f *dbFlags, log *slog.Logger) (*db.DB, error) {
	dsn, err := f.resolveDBURL()
	if err != nil {
		return nil, emitError("invalid", "resolve db url: "+err.Error(), nil)
	}
	summary := f.connectionSummary(dsn)
	log.Info("db open", "host", summary["host"], "db", summary["db"], "sslmode", summary["sslmode"])
	d, err := db.Open(ctx, dsn)
	if err != nil {
		return nil, emitError("db", "open db: "+redactURL(err.Error()), summary)
	}
	return d, nil
}

// parseSummary extracts host, db name, and sslmode from a DSN. Best-effort —
// returns "?" on parse failure. Never returns userinfo.
func parseSummary(dsn string) (host, dbName, sslmode string) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "?", "?", "?"
	}
	host = u.Hostname()
	if host == "" {
		host = "?"
	}
	dbName = strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		dbName = "?"
	}
	sslmode = u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "?"
	}
	return
}
