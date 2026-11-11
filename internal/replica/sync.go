package replica

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"foryou.local/foryou/internal/sdk"
)

const syMaxPages = 20

type sySyncState struct {
	mu         sync.Mutex
	retryAfter map[string]time.Time
}

func init() {
	routeGroups = append(routeGroups, func(s *Service, _ *http.ServeMux) {
		state := &sySyncState{retryAfter: make(map[string]time.Time)}
		s.Every("sync", s.Config().PollInterval, func(ctx context.Context, _ time.Time) error {
			return sySync(ctx, s, state)
		})
	})
}

func sySync(ctx context.Context, s *Service, state *sySyncState) error {
	for _, source := range s.Sources() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		state.mu.Lock()
		retryAt := state.retryAfter[source.ProviderID]
		state.mu.Unlock()
		if s.Now().Before(retryAt) {
			continue
		}
		client, err := sdk.New(source.Origin, sdk.Options{ServiceID: source.ProviderID, AllowLoopbackHTTP: s.Dev(), Timeout: 10 * time.Second})
		if err != nil {
			slog.Error("replica sync source failed", "origin", source.Origin, "provider_id", source.ProviderID, "error", err)
			continue
		}
		for pageNumber := 0; pageNumber < syMaxPages; pageNumber++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			after := s.Records().Cursor(source.ProviderID)
			result, err := client.Changes(ctx, after, 100)
			if err != nil {
				var response *sdk.Error
				if errors.As(err, &response) && (response.HTTPStatus == http.StatusTooManyRequests || response.HTTPStatus == http.StatusServiceUnavailable) {
					delay := response.RetryAfter
					if delay < time.Second || delay > 24*time.Hour {
						delay = 60 * time.Second
					}
					state.mu.Lock()
					state.retryAfter[source.ProviderID] = s.Now().Add(delay)
					state.mu.Unlock()
				}
				slog.Error("replica sync source failed", "origin", source.Origin, "provider_id", source.ProviderID, "error", err)
				break
			}
			copies, failures, err := syVerifyPage(result, source.ProviderID, s.Now())
			if err != nil {
				slog.Error("replica sync page failed", "origin", source.Origin, "provider_id", source.ProviderID, "error", err)
				break
			}
			for _, failure := range failures {
				slog.Warn("replica skipped invalid source record", "origin", source.Origin, "provider_id", source.ProviderID, "sequence", failure.sequence, "error", failure.err)
			}
			if _, err := s.Records().Import(source.ProviderID, copies, uint64(result.Page.NextAfter)); err != nil {
				slog.Error("replica sync import failed", "origin", source.Origin, "provider_id", source.ProviderID, "error", err)
				break
			}
			if !result.Page.More {
				break
			}
		}
	}
	return nil
}
