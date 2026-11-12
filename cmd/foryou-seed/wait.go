package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type settlementPoll func(context.Context, string) (bool, error)

func waitForReceipts(ctx context.Context, ids []string, interval time.Duration, settled settlementPoll) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	remaining := append([]string(nil), ids...)
	for len(remaining) > 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out with %d unsettled receipts, first %v: %w", len(remaining), remaining[:min(len(remaining), 5)], ctx.Err())
		case <-ticker.C:
		}
		for i, id := range remaining {
			ok, err := settled(ctx, id)
			if err != nil {
				if errors.Is(err, errNotFound) {
					remaining = append([]string(nil), remaining[i:]...)
					break
				}
				return fmt.Errorf("settlement receipt %s: %w", id, err)
			}
			if !ok {
				remaining = append([]string(nil), remaining[i:]...)
				break
			}
			if i == len(remaining)-1 {
				remaining = nil
			}
		}
	}
	return nil
}

var errNotFound = errors.New("not found")
