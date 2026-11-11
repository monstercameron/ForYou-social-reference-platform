package provider

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/store"
)

const (
	// namesMaxSnapshots is the disclosed cap of 256 live lookup snapshots.
	namesMaxSnapshots = 256
	// namesMaxMatches is the disclosed cap of 10,000 matching authors per lookup.
	namesMaxMatches = 10000
	// namesSnapshotLife is the disclosed snapshot lifetime of 15 minutes.
	namesSnapshotLife       = 15 * time.Minute
	namesPageSize           = 50
	namesLookupRetrySeconds = 60
)

type namesSnapshot struct {
	query     string
	authors   []string
	expires   time.Time
	positions map[int]string
}

type namesCursorRef struct {
	snapshot *namesSnapshot
	position int
}

type namesSnapshotSet struct {
	mu      sync.Mutex
	entries map[*namesSnapshot]struct{}
	cursors map[string]namesCursorRef
	pending int
}

var (
	errNamesUnknownCursor  = errors.New("provider: unknown name cursor")
	errNamesSnapshotsFull  = errors.New("provider: name snapshots full")
	errNamesTooManyMatches = errors.New("provider: too many name matches")
)

func (s *Service) namesLookupRoute(set *namesSnapshotSet, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.LimitRead(w, r, ReadListing) {
		return
	}
	query, cursor, status := namesParseLookupQuery(r.URL.RawQuery)
	if status != "" {
		writeError(w, http.StatusBadRequest, status)
		return
	}
	fold := names.FoldKey(query)
	if cursor != "" {
		page, err := s.namesContinueLookup(set, fold, cursor)
		if err != nil {
			if errors.Is(err, errNamesUnknownCursor) {
				writeError(w, http.StatusBadRequest, "invalid_cursor")
			} else {
				writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
			}
			return
		}
		writeCanonical(w, http.StatusOK, page, "no-store")
		return
	}
	authors, err := s.namesLookupAuthors(fold, namesPageSize)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if len(authors) <= namesPageSize {
		page, err := s.namesBuildPage(authors, fold, "")
		if err != nil {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
			return
		}
		writeCanonical(w, http.StatusOK, page, "no-store")
		return
	}
	page, err := s.namesStartLookup(set, fold)
	if err != nil {
		if errors.Is(err, errNamesSnapshotsFull) {
			writeRetry(w, http.StatusTooManyRequests, "rate_limited", namesLookupRetrySeconds)
		} else if errors.Is(err, errNamesTooManyMatches) {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", namesLookupRetrySeconds)
		} else {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		}
		return
	}
	writeCanonical(w, http.StatusOK, page, "no-store")
}

func namesParseLookupQuery(raw string) (string, string, string) {
	if len(raw) > 16384 || !utf8.ValidString(raw) {
		return "", "", "invalid_query"
	}
	var query, cursor string
	hasQuery, hasCursor := false, false
	if raw == "" {
		return "", "", "invalid_query"
	}
	for _, component := range strings.Split(raw, "&") {
		key, value, found := strings.Cut(component, "=")
		if !found || key != "display_name" && key != "cursor" {
			return "", "", "invalid_query"
		}
		decoded, ok := namesDecodeQueryValue(value)
		if !ok {
			return "", "", "invalid_query"
		}
		if key == "display_name" {
			if hasQuery {
				return "", "", "invalid_query"
			}
			hasQuery, query = true, decoded
		} else {
			if hasCursor {
				return "", "", "invalid_query"
			}
			hasCursor, cursor = true, decoded
		}
	}
	if !hasQuery {
		return "", "", "invalid_query"
	}
	query, err := names.CheckQuery(query)
	if err != nil {
		return "", "", "invalid_query"
	}
	if hasCursor && !namesValidCursor(cursor) {
		return "", "", "invalid_cursor"
	}
	return query, cursor, ""
}

func namesDecodeQueryValue(value string) (string, bool) {
	decoded := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			if value[i] > 0x7f || value[i] != '+' && !namesUnreserved(value[i]) {
				return "", false
			}
			decoded = append(decoded, value[i])
			continue
		}
		if i+2 >= len(value) || !namesUpperHex(value[i+1]) || !namesUpperHex(value[i+2]) {
			return "", false
		}
		decodedByte := namesHex(value[i+1])<<4 | namesHex(value[i+2])
		if namesUnreserved(decodedByte) {
			return "", false
		}
		decoded = append(decoded, decodedByte)
		i += 2
	}
	if !utf8.Valid(decoded) {
		return "", false
	}
	for i := 0; i+2 < len(decoded); i++ {
		if decoded[i] == '%' && namesHexValid(decoded[i+1]) && namesHexValid(decoded[i+2]) {
			return "", false
		}
	}
	return string(decoded), true
}

func namesUnreserved(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value))
}

func namesUpperHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'A' && value <= 'F'
}

func namesHexValid(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func namesHex(value byte) byte {
	if value >= '0' && value <= '9' {
		return value - '0'
	}
	return value - 'A' + 10
}

func namesValidCursor(cursor string) bool {
	if len(cursor) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == cursor
}

func (s *Service) namesLookupAuthors(fold string, maximum int) ([]string, error) {
	authors := make([]string, 0)
	err := s.Store().View(func(tx store.Tx) error {
		return tx.Scan(namesFoldCollection, store.Prefix(fold+"\x1f"), func(key string, _ []byte) (bool, error) {
			authors = append(authors, strings.TrimPrefix(key, fold+"\x1f"))
			return len(authors) <= maximum, nil
		})
	})
	if err != nil {
		return nil, err
	}
	return authors, nil
}

func (s *Service) namesBuildPage(authors []string, fold, next string) ([]byte, error) {
	page := names.NamesPage{Names: make([]names.NameRecord, 0, namesPageSize)}
	err := s.Store().View(func(tx store.Tx) error {
		for _, author := range authors {
			record, _, found, err := namesLoadCurrent(tx, author)
			if err != nil {
				return err
			}
			if found && record.DisplayName != nil && names.FoldKey(*record.DisplayName) == fold {
				page.Names = append(page.Names, record)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if next != "" {
		page.NextCursor = &next
	}
	return names.EncodePage(page)
}

func (s *Service) namesStartLookup(set *namesSnapshotSet, fold string) ([]byte, error) {
	set.mu.Lock()
	now := s.Now()
	namesEvictSnapshots(set, now)
	if len(set.entries)+set.pending >= namesMaxSnapshots {
		set.mu.Unlock()
		return nil, errNamesSnapshotsFull
	}
	set.pending++
	set.mu.Unlock()

	authors, err := s.namesLookupAuthors(fold, namesMaxMatches)
	set.mu.Lock()
	set.pending--
	if err != nil {
		set.mu.Unlock()
		return nil, err
	}
	if len(authors) > namesMaxMatches {
		set.mu.Unlock()
		return nil, errNamesTooManyMatches
	}
	namesEvictSnapshots(set, s.Now())
	snapshot := &namesSnapshot{query: fold, authors: append([]string(nil), authors...), expires: now.Add(namesSnapshotLife), positions: make(map[int]string)}
	set.entries[snapshot] = struct{}{}
	next, err := namesCursor(set, snapshot, namesPageSize)
	if err != nil {
		namesRemoveSnapshot(set, snapshot)
		set.mu.Unlock()
		return nil, err
	}
	pageAuthors, _ := namesPageAuthors(snapshot.authors, 0)
	page, err := s.namesBuildPage(pageAuthors, fold, next)
	if err != nil {
		namesRemoveSnapshot(set, snapshot)
		set.mu.Unlock()
		return nil, err
	}
	set.mu.Unlock()
	return page, nil
}

func (s *Service) namesContinueLookup(set *namesSnapshotSet, fold, cursor string) ([]byte, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	namesEvictSnapshots(set, s.Now())
	ref, ok := set.cursors[cursor]
	if !ok || ref.snapshot.query != fold || !s.Now().Before(ref.snapshot.expires) {
		return nil, errNamesUnknownCursor
	}
	position := ref.position
	pageNames, end := namesPageAuthors(ref.snapshot.authors, position)
	next := ""
	if end < len(ref.snapshot.authors) {
		var err error
		next, err = namesCursor(set, ref.snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	page, err := s.namesBuildPage(pageNames, fold, next)
	return page, err
}

func namesPageAuthors(authors []string, position int) ([]string, int) {
	end := position + namesPageSize
	if end > len(authors) {
		end = len(authors)
	}
	return authors[position:end], end
}

func namesCursor(set *namesSnapshotSet, snapshot *namesSnapshot, position int) (string, error) {
	if cursor := snapshot.positions[position]; cursor != "" {
		return cursor, nil
	}
	for {
		raw := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, raw); err != nil {
			return "", err
		}
		cursor := base64.RawURLEncoding.EncodeToString(raw)
		if _, exists := set.cursors[cursor]; exists {
			continue
		}
		snapshot.positions[position] = cursor
		set.cursors[cursor] = namesCursorRef{snapshot: snapshot, position: position}
		return cursor, nil
	}
}

func namesEvictSnapshots(set *namesSnapshotSet, now time.Time) {
	for snapshot := range set.entries {
		if !now.Before(snapshot.expires) {
			namesRemoveSnapshot(set, snapshot)
		}
	}
}

func namesRemoveSnapshot(set *namesSnapshotSet, snapshot *namesSnapshot) {
	delete(set.entries, snapshot)
	for _, cursor := range snapshot.positions {
		delete(set.cursors, cursor)
	}
}
