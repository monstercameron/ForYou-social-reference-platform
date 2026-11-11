package provider

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

// NamesChangeInterval is the profile's required minimum interval between accepted changes. The profile requires a provider to disclose any looser limit; this provider uses exactly the profile's limit.
const NamesChangeInterval = 24 * time.Hour

const (
	namesCurrentCollection = "provider.names.current"
	namesChangedCollection = "provider.names.changed"
	namesFoldCollection    = "provider.names.fold"
)

func namesFoldEntry(record names.NameRecord) string {
	if record.DisplayName == nil {
		return ""
	}
	return names.FoldKey(*record.DisplayName) + "\x1f" + record.AuthorID
}

func namesLoadCurrent(tx store.Tx, authorID string) (names.NameRecord, []byte, bool, error) {
	encoded, ok, err := tx.Get(namesCurrentCollection, authorID)
	if err != nil || !ok {
		return names.NameRecord{}, nil, ok, err
	}
	record, err := names.Parse(encoded)
	if err != nil || record.AuthorID != authorID {
		return names.NameRecord{}, nil, false, errors.New("provider: invalid stored name record")
	}
	return record, encoded, true, nil
}

// namesPublishResult carries the outcome of one serialized name-record acceptance.
type namesPublishResult struct {
	status int
	signal string
	body   []byte
	retry  int
}

// namesAcceptedPost checks an unbounded AuthorMessages query; author_id is the root key also for delegate-signed posts. Expired or removed records no longer count.
func (s *Service) namesAcceptedPost(authorID string) bool {
	return len(s.Records().Select(discovery.Query{Route: discovery.AuthorMessages, AuthorID: authorID, Limit: 1})) > 0
}

func (s *Service) namesPublish(record names.NameRecord, encoded []byte, now time.Time) namesPublishResult {
	result := namesPublishResult{}
	err := s.Store().Update(func(tx store.Tx) error {
		held, heldBytes, ok, err := namesLoadCurrent(tx, record.AuthorID)
		if err != nil {
			return err
		}
		if ok && bytes.Equal(heldBytes, encoded) {
			result.status, result.body = http.StatusOK, heldBytes
			return nil
		}
		if ok && names.Newer(held, record) {
			result.status, result.signal = http.StatusConflict, "stale_state"
			return nil
		}
		if !s.namesAcceptedPost(record.AuthorID) {
			result.status, result.signal = http.StatusConflict, "not_ready"
			return nil
		}
		changedBytes, exists, err := tx.Get(namesChangedCollection, record.AuthorID)
		if err != nil {
			return err
		}
		if exists {
			changed, err := wire.ParseTime(string(changedBytes))
			if err != nil {
				return fmt.Errorf("provider: invalid stored name change time: %w", err)
			}
			elapsed := now.Sub(changed)
			if elapsed < NamesChangeInterval {
				remaining := NamesChangeInterval - elapsed
				result.retry = int((remaining + time.Second - 1) / time.Second)
				if result.retry < 1 {
					result.retry = 1
				}
				result.status, result.signal = http.StatusTooManyRequests, "rate_limited"
				return nil
			}
		}
		if ok {
			if oldFold := namesFoldEntry(held); oldFold != "" {
				if err := tx.Delete(namesFoldCollection, oldFold); err != nil {
					return err
				}
			}
		}
		if err := tx.Put(namesCurrentCollection, record.AuthorID, encoded); err != nil {
			return err
		}
		if err := tx.Put(namesChangedCollection, record.AuthorID, []byte(wire.FormatTime(now))); err != nil {
			return err
		}
		if fold := namesFoldEntry(record); fold != "" {
			if err := tx.Put(namesFoldCollection, fold, []byte("1")); err != nil {
				return err
			}
		}
		result.status, result.body = http.StatusCreated, encoded
		return nil
	})
	if err != nil {
		return namesPublishResult{status: http.StatusServiceUnavailable, signal: "unavailable", retry: apierr.DefaultRetryAfterSeconds}
	}
	return result
}
