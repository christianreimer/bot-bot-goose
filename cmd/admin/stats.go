package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"
)

func runStats(ctx context.Context, log *slog.Logger) error {
	if len(os.Args) < 2 {
		statsUsage()
		os.Exit(2)
	}
	verb := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	switch verb {
	case "overview":
		return statsOverview(ctx, log)
	case "players":
		return statsPlayers(ctx, log)
	case "decoys":
		return statsDecoys(ctx, log)
	case "prelaunch":
		return statsPrelaunch(ctx, log)
	default:
		statsUsage()
		os.Exit(2)
	}
	return nil
}

func statsUsage() {
	fmt.Fprintln(os.Stderr, `usage: bbg-admin stats <verb> [flags]
  overview  Snapshot: players, plays, decoys, prelaunch, pool inventory.
  players   Daily time series: active devices + plays started/completed.
  decoys    Daily time series: decoy submissions by status (+ --top fool-rate).
  prelaunch   Daily time series: prelaunch submissions / ingested / rejected.`)
}

// registerStatsWindow adds --since and --days. --since overrides --days. If
// neither is set, default is 30 days back, midnight UTC.
func registerStatsWindow(fs *flag.FlagSet) (*string, *int) {
	since := fs.String("since", "", "YYYY-MM-DD (overrides --days)")
	days := fs.Int("days", 30, "window size: now - days")
	return since, days
}

func resolveSince(sinceStr string, days int) (time.Time, error) {
	if sinceStr != "" {
		t, err := time.Parse("2006-01-02", sinceStr)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse --since: %w", err)
		}
		return t.UTC(), nil
	}
	if days < 1 {
		return time.Time{}, fmt.Errorf("--days must be >= 1")
	}
	return time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour), nil
}

// --- overview ----------------------------------------------------------------

func statsOverview(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("stats overview", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	sinceStr, days := registerStatsWindow(fs)
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	since, err := resolveSince(*sinceStr, *days)
	if err != nil {
		return emitError("invalid", err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	s, err := d.StatOverview(ctx, since)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		fmt.Fprintf(os.Stdout, "Window: since %s (~%d days)\n\n", s.Since.Format("2006-01-02"), s.WindowDays)
		fmt.Fprintln(os.Stdout, "PLAYERS")
		rows := [][]any{
			{"active_devices", s.ActiveDevices},
			{"plays_started", s.PlaysStarted},
			{"plays_completed", s.PlaysCompleted},
			{"avg_score_pct", fmtFloatPtr(s.AvgScore, "-")},
		}
		_ = emitTable([]string{"METRIC", "VALUE"}, rows)
		fmt.Fprintln(os.Stdout, "\nDECOYS (window)")
		rows = [][]any{
			{"submitted", s.DecoysSubmittedTotal},
			{"approved", s.DecoysApproved},
			{"pending", s.DecoysPending},
			{"rejected", s.DecoysRejected},
		}
		_ = emitTable([]string{"METRIC", "VALUE"}, rows)
		fmt.Fprintln(os.Stdout, "\nPRELAUNCH (window)")
		rows = [][]any{
			{"submitted", s.PrelaunchSubmitted},
			{"ingested", s.PrelaunchIngested},
			{"rejected", s.PrelaunchRejected},
			{"unique_devices", s.PrelaunchUniqueDevices},
		}
		_ = emitTable([]string{"METRIC", "VALUE"}, rows)
		fmt.Fprintln(os.Stdout, "\nPOOL INVENTORY (current, all-time)")
		rows = [][]any{
			{"approved_decoys", s.ApprovedDecoyPool},
			{"approved_bots", s.ApprovedBotPool},
			{"pending_decoys", s.PendingDecoyPool},
			{"pending_bots", s.PendingBotPool},
			{"pending_prelaunch", s.PendingPrelaunch},
		}
		return emitTable([]string{"METRIC", "VALUE"}, rows)
	}
	return emitJSON(map[string]any{
		"since":       s.Since.Format("2006-01-02"),
		"window_days": s.WindowDays,
		"players": map[string]any{
			"active_devices":   s.ActiveDevices,
			"plays_started":    s.PlaysStarted,
			"plays_completed":  s.PlaysCompleted,
			"avg_score_pct":    s.AvgScore,
		},
		"decoys": map[string]any{
			"submitted": s.DecoysSubmittedTotal,
			"approved":  s.DecoysApproved,
			"pending":   s.DecoysPending,
			"rejected":  s.DecoysRejected,
		},
		"prelaunch": map[string]any{
			"submitted":      s.PrelaunchSubmitted,
			"ingested":       s.PrelaunchIngested,
			"rejected":       s.PrelaunchRejected,
			"unique_devices": s.PrelaunchUniqueDevices,
		},
		"pool_inventory": map[string]any{
			"approved_decoys": s.ApprovedDecoyPool,
			"approved_bots":   s.ApprovedBotPool,
			"pending_decoys":  s.PendingDecoyPool,
			"pending_bots":    s.PendingBotPool,
			"pending_prelaunch": s.PendingPrelaunch,
		},
	})
}

// --- players -----------------------------------------------------------------

func statsPlayers(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("stats players", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	sinceStr, days := registerStatsWindow(fs)
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	since, err := resolveSince(*sinceStr, *days)
	if err != nil {
		return emitError("invalid", err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	rows, err := d.PlayersByDay(ctx, since)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		out := make([][]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				r.Day.Format("2006-01-02"),
				r.ActiveDevices, r.PlaysStarted, r.PlaysCompleted,
				fmtFloatPtr(r.AvgScore, "-"),
			})
		}
		return emitTable([]string{"DAY", "ACTIVE", "STARTED", "COMPLETED", "AVG_SCORE"}, out)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"day":             r.Day.Format("2006-01-02"),
			"active_devices":  r.ActiveDevices,
			"plays_started":   r.PlaysStarted,
			"plays_completed": r.PlaysCompleted,
			"avg_score_pct":   r.AvgScore,
		})
	}
	return emitJSON(out)
}

// --- decoys ------------------------------------------------------------------

func statsDecoys(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("stats decoys", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	sinceStr, days := registerStatsWindow(fs)
	top := fs.Int("top", 0, "if >0, return the N highest-fool-rate decoys instead of the time series")
	minImpressions := fs.Int("min-impressions", 25, "minimum impressions for --top to consider a decoy (filters noise)")
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	since, err := resolveSince(*sinceStr, *days)
	if err != nil {
		return emitError("invalid", err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	if *top > 0 {
		rows, err := d.TopDecoysByFoolRate(ctx, since, *top, *minImpressions)
		if err != nil {
			return emitError("db", err.Error(), nil)
		}
		if *asTable {
			out := make([][]any, 0, len(rows))
			for _, r := range rows {
				out = append(out, []any{
					r.DecoyID.String()[:8], r.Impressions, r.PickedAsBot,
					fmtFloatPtr(r.FoolRate, "-"),
					truncate(r.PromptText, 25), truncate(r.Text, 40),
				})
			}
			return emitTable([]string{"DECOY", "IMP", "PICKED", "FOOL_RATE", "PROMPT", "TEXT"}, out)
		}
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{
				"decoy_id":     r.DecoyID.String(),
				"prompt_text":  r.PromptText,
				"text":         r.Text,
				"impressions":  r.Impressions,
				"picked_as_bot": r.PickedAsBot,
				"fool_rate":    r.FoolRate,
			})
		}
		return emitJSON(out)
	}
	rows, err := d.DecoysByDay(ctx, since)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		out := make([][]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				r.Day.Format("2006-01-02"),
				r.Submitted, r.Approved, r.Pending, r.Rejected,
			})
		}
		return emitTable([]string{"DAY", "SUBMITTED", "APPROVED", "PENDING", "REJECTED"}, out)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"day":       r.Day.Format("2006-01-02"),
			"submitted": r.Submitted,
			"approved":  r.Approved,
			"pending":   r.Pending,
			"rejected":  r.Rejected,
		})
	}
	return emitJSON(out)
}

// --- prelaunch -----------------------------------------------------------------

func statsPrelaunch(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("stats prelaunch", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	sinceStr, days := registerStatsWindow(fs)
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	since, err := resolveSince(*sinceStr, *days)
	if err != nil {
		return emitError("invalid", err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	rows, err := d.PrelaunchByDay(ctx, since)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		out := make([][]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, []any{
				r.Day.Format("2006-01-02"),
				r.Submitted, r.Ingested, r.Rejected, r.StillPending, r.UniqueDevices,
			})
		}
		return emitTable([]string{"DAY", "SUBMITTED", "INGESTED", "REJECTED", "PENDING", "DEVICES"}, out)
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"day":            r.Day.Format("2006-01-02"),
			"submitted":      r.Submitted,
			"ingested":       r.Ingested,
			"rejected":       r.Rejected,
			"still_pending":  r.StillPending,
			"unique_devices": r.UniqueDevices,
		})
	}
	return emitJSON(out)
}

// --- helpers -----------------------------------------------------------------

func fmtFloatPtr(f *float64, missing string) string {
	if f == nil {
		return missing
	}
	return fmt.Sprintf("%.2f", *f)
}
