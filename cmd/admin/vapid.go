package main

import (
	"context"
	"fmt"
	"log/slog"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// runVAPIDGen prints a fresh VAPID key pair to stdout — feed into
// BBG_VAPID_PUBLIC / BBG_VAPID_PRIVATE in deploy/compose.env. The plan
// requires rotation; store both current and next when the time comes.
func runVAPIDGen(_ context.Context, _ *slog.Logger) error {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	fmt.Printf("BBG_VAPID_PUBLIC=%s\nBBG_VAPID_PRIVATE=%s\n", pub, priv)
	return nil
}
