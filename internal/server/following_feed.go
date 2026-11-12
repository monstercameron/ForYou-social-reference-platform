package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	followingPageSize       = 20
	followingFanout         = 8
	followingPageBudget     = 10 * time.Second
	followingCursorLimit    = 8
	followingCursorLifetime = 15 * time.Minute
	followingCoverageNote   = "Showing posts from your sources. Some posts may be missing."
)

// followingCursor holds the merged posts a Following page has not shown yet.
// It belongs to one profile and one follow list revision, and expires.
type followingCursor struct {
	profile  string
	revision int
	expires  time.Time
	pending  []post
	coverage Coverage
}

var followingCursors = struct {
	sync.Mutex
	byToken map[string]*followingCursor
}{byToken: make(map[string]*followingCursor)}

// followingFeed is one batch of the Following feed and what it leaves to load.
type followingFeed struct {
	// State is empty for a list with posts, or none, empty, error or expired.
	State    string
	Posts    []post
	Cursor   string
	Coverage Coverage
}

// followingParallel runs task for each index with at most followingFanout in flight.
func followingParallel(n int, task func(int)) {
	slots := make(chan struct{}, followingFanout)
	var wait sync.WaitGroup
	for i := range n {
		wait.Add(1)
		go func() {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			task(i)
		}()
	}
	wait.Wait()
}

// followingCollect asks each followed key's author lookup and merges the
// results by signed timestamp, newest first, then by message ID. It returns
// how many lookups failed so the caller can tell a dead set of sources apart.
func (a *App) followingCollect(r *http.Request, follows []followingEntry) ([]post, Coverage, int) {
	ctx, cancel := context.WithTimeout(r.Context(), followingPageBudget)
	defer cancel()
	pages := make([]recordPage, len(follows))
	errs := make([]error, len(follows))
	followingParallel(len(follows), func(i int) {
		pages[i], errs[i] = a.records().ByAuthor(ctx, r, follows[i].AuthorID)
	})
	var merged []post
	var coverages []Coverage
	seen := make(map[string]bool)
	failed := 0
	for i, page := range pages {
		if errs[i] != nil {
			failed++
			continue
		}
		coverages = append(coverages, page.Coverage)
		if !followingAnswered(page) {
			failed++
			continue
		}
		for _, p := range page.Posts {
			if p.AuthorID != follows[i].AuthorID || seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			merged = append(merged, p)
		}
	}
	slices.SortStableFunc(merged, func(x, y post) int {
		if c := strings.Compare(y.Timestamp, x.Timestamp); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return merged, followingMergeCoverage(coverages), failed
}

// followingAnswered reports whether a key's lookup reached a source. This
// server's local copy has no endpoint and always answers, so it does not
// count: a page without posts whose remote sources all failed is a failed
// lookup, not an empty one.
func followingAnswered(page recordPage) bool {
	remote := slices.DeleteFunc(slices.Clone(page.Coverage.Sources), func(s SourceStatus) bool { return s.Endpoint == "" })
	return len(page.Posts) > 0 || len(remote) == 0 || slices.ContainsFunc(remote, func(s SourceStatus) bool { return s.OK })
}

// followingMergeCoverage keeps each distinct source status once, under the
// Following note that never claims completeness.
func followingMergeCoverage(list []Coverage) Coverage {
	coverage := Coverage{Note: followingCoverageNote}
	seen := make(map[string]bool)
	for _, c := range list {
		for _, source := range c.Sources {
			key := source.Label + "\x00" + source.Endpoint + "\x00" + source.Text
			if !seen[key] {
				seen[key] = true
				coverage.Sources = append(coverage.Sources, source)
			}
		}
	}
	return coverage
}

// followingFirstFeed builds the first batch for a profile's follows.
func (a *App) followingFirstFeed(r *http.Request, profile string, state followingState) followingFeed {
	if len(state.Follows) == 0 {
		return followingFeed{State: "none"}
	}
	merged, coverage, failed := a.followingCollect(r, state.Follows)
	if failed == len(state.Follows) {
		return followingFeed{State: "error", Coverage: coverage}
	}
	if len(merged) == 0 {
		return followingFeed{State: "empty", Coverage: coverage}
	}
	first := merged[:min(followingPageSize, len(merged))]
	feed := followingFeed{Posts: first, Coverage: coverage}
	if rest := merged[len(first):]; len(rest) > 0 {
		token, err := a.followingRemember(profile, state.Revision, rest, coverage)
		if err != nil {
			return followingFeed{State: "error", Coverage: coverage}
		}
		feed.Cursor = token
	}
	a.forReader(r, feed.Posts)
	return feed
}

// followingNextFeed returns the next batch behind a cursor, or an expired state.
func (a *App) followingNextFeed(r *http.Request, profile string, state followingState, token string) followingFeed {
	batch, coverage, more, ok := a.followingTake(profile, token, state.Revision, followingPageSize)
	if !ok {
		return followingFeed{State: "expired"}
	}
	a.forReader(r, batch)
	return followingFeed{Posts: batch, Cursor: more, Coverage: coverage}
}

// followingRemember stores the pending posts behind a new random cursor.
// A profile keeps at most followingCursorLimit cursors; the oldest give way.
func (a *App) followingRemember(profile string, revision int, pending []post, coverage Coverage) (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	now := a.now()
	followingCursors.Lock()
	defer followingCursors.Unlock()
	for key, cursor := range followingCursors.byToken {
		if !now.Before(cursor.expires) {
			delete(followingCursors.byToken, key)
		}
	}
	for {
		var oldest string
		count := 0
		for key, cursor := range followingCursors.byToken {
			if cursor.profile != profile {
				continue
			}
			count++
			if oldest == "" || cursor.expires.Before(followingCursors.byToken[oldest].expires) {
				oldest = key
			}
		}
		if count < followingCursorLimit {
			break
		}
		delete(followingCursors.byToken, oldest)
	}
	followingCursors.byToken[token] = &followingCursor{profile: profile, revision: revision, expires: now.Add(followingCursorLifetime), pending: pending, coverage: coverage}
	return token, nil
}

// followingTake removes up to size posts from a cursor. A cursor that is
// unknown, expired, of another profile or of an older follow list is not ok.
func (a *App) followingTake(profile, token string, revision, size int) ([]post, Coverage, string, bool) {
	now := a.now()
	followingCursors.Lock()
	defer followingCursors.Unlock()
	cursor, found := followingCursors.byToken[token]
	if !found || cursor.profile != profile || cursor.revision != revision || !now.Before(cursor.expires) {
		delete(followingCursors.byToken, token)
		return nil, Coverage{}, "", false
	}
	batch := slices.Clone(cursor.pending[:min(size, len(cursor.pending))])
	cursor.pending = cursor.pending[len(batch):]
	if len(cursor.pending) == 0 {
		delete(followingCursors.byToken, token)
		return batch, cursor.coverage, "", true
	}
	return batch, cursor.coverage, token, true
}
