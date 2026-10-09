package ingest

import (
	"context"
	"fmt"
	"time"
	"trie-trunk/go/server/store"
)

func (h *Handler) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("positive flush interval required")
	}
	// Open persisted taps even when idle, so rolling trie statistics age out
	// after restart without waiting for another event to arrive.
	taps, err := store.ListTaps(ctx, h.config.Root)
	if err != nil {
		return err
	}
	for _, tap := range taps {
		if _, err = h.openTap(tap); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return h.Flush()
		case <-ticker.C:
			if e := h.Flush(); e != nil {
				return e
			}
		}
	}
}
