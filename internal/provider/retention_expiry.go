package provider

import (
	"context"
	"log"
	"net/http"
	"time"
)

func init() {
	routeGroups = append(routeGroups, retentionExpiry)
}

func retentionExpiry(s *Service, _ *http.ServeMux) {
	s.Every("retention-expiry", time.Hour, func(_ context.Context, now time.Time) error {
		if err := s.CheckClock(); err != nil {
			return err
		}
		for _, id := range s.Records().DueForExpiry(now) {
			if err := s.Records().Expire(id, now); err != nil {
				log.Printf("provider: retention expiry for %s: %v", id, err)
			}
		}
		return nil
	})
}
