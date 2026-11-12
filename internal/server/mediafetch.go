package server

import (
	"container/list"
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/wire"
)

const (
	medCacheLimit   = 256 << 20
	medConcurrency  = 4
	medFetchTimeout = 30 * time.Second
)

// medFetchOverride lets tests point the isolated client at a local fake. Production leaves it nil.
var medFetchOverride func(*content.FetchConfig)

// medKey identifies a verified object by its signed digest and size.
type medKey struct {
	sha  string
	size int64
}

type medEntry struct {
	key   medKey
	media content.VerifiedMedia
}

// medCache holds verified objects in memory, least recently used first to go.
type medCache struct {
	mu    sync.Mutex
	size  int64
	order *list.List
	items map[medKey]*list.Element
}

func newMedCache() *medCache {
	return &medCache{order: list.New(), items: map[medKey]*list.Element{}}
}

func (c *medCache) get(key medKey) (content.VerifiedMedia, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		return content.VerifiedMedia{}, false
	}
	c.order.MoveToFront(element)
	return element.Value.(medEntry).media, true
}

func (c *medCache) put(key medKey, media content.VerifiedMedia) {
	c.mu.Lock()
	defer c.mu.Unlock()
	size := int64(len(media.Bytes))
	if _, ok := c.items[key]; ok || size > medCacheLimit {
		return
	}
	c.items[key] = c.order.PushFront(medEntry{key: key, media: media})
	c.size += size
	for c.size > medCacheLimit {
		oldest := c.order.Back()
		entry := oldest.Value.(medEntry)
		c.order.Remove(oldest)
		delete(c.items, entry.key)
		c.size -= int64(len(entry.media.Bytes))
	}
}

type medFlightKey struct {
	url, sha string
}

// medFlight is one fetch and the callers waiting on it. It records the redirect origins it followed
// and the one it refused, so a caller with other consent can tell whether the result is one it may take.
type medFlight struct {
	key     medFlightKey
	done    chan struct{}
	cancel  context.CancelFunc
	origins []string
	touched []string
	media   content.VerifiedMedia
	moved   string
	err     error
}

// usableBy reports whether a caller that approves the origins allowed may take this flight's result.
func (fl *medFlight) usableBy(allowed func(origin string) bool) bool {
	if fl.moved != "" && allowed(fl.moved) {
		return false
	}
	for _, origin := range fl.touched {
		if !allowed(origin) {
			return false
		}
	}
	return true
}

// wait returns once the flight has finished, or with ctx's error when the caller stops waiting first.
func (fl *medFlight) wait(ctx context.Context) error {
	select {
	case <-fl.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// medFetcher runs isolated fetches with a bounded number in flight. Each flight is registered under every
// origin it contacts, so revoking an origin cancels the fetches that would still reach it.
type medFetcher struct {
	cache    *medCache
	mu       sync.Mutex
	flights  map[medFlightKey]*medFlight
	byOrigin map[string]map[*medFlight]bool
	slots    chan struct{}
}

func newMedFetcher(cache *medCache) *medFetcher {
	return &medFetcher{cache: cache, flights: map[medFlightKey]*medFlight{}, byOrigin: map[string]map[*medFlight]bool{}, slots: make(chan struct{}, medConcurrency)}
}

// load returns the verified object for slot. allowed says whether a redirect to an origin is approved
// for this caller; a refused origin is reported as moved. A caller joins a fetch already running for the
// same URL and digest only when that fetch's result is one it may take, otherwise it runs its own fetch.
// The fetch runs under its own deadline, so a caller that stops waiting does not cancel the flight others share.
// An object already verified and held is returned without contacting anything.
func (f *medFetcher) load(ctx context.Context, slot wire.MediaReference, allowed func(origin string) bool) (content.VerifiedMedia, string, error) {
	if media, ok := f.cache.get(medKey{sha: slot.SHA256, size: slot.SizeBytes}); ok {
		return media, "", nil
	}
	key := medFlightKey{url: slot.URL, sha: slot.SHA256}
	f.mu.Lock()
	flight, joined := f.flights[key]
	if !joined {
		flight = f.startLocked(slot, allowed, true)
	}
	f.mu.Unlock()
	if err := flight.wait(ctx); err != nil {
		return content.VerifiedMedia{}, "", err
	}
	if joined && !flight.usableBy(allowed) {
		f.mu.Lock()
		flight = f.startLocked(slot, allowed, false)
		f.mu.Unlock()
		if err := flight.wait(ctx); err != nil {
			return content.VerifiedMedia{}, "", err
		}
	}
	return flight.media, flight.moved, flight.err
}

// startLocked begins a fetch on its own goroutine. A shared flight is the one later callers join while it runs.
func (f *medFetcher) startLocked(slot wire.MediaReference, allowed func(origin string) bool, shared bool) *medFlight {
	ctx, cancel := context.WithTimeout(context.Background(), medFetchTimeout)
	flight := &medFlight{key: medFlightKey{url: slot.URL, sha: slot.SHA256}, done: make(chan struct{}), cancel: cancel}
	if shared {
		f.flights[flight.key] = flight
	}
	if origin, _, ok := medOriginOf(slot.URL); ok {
		f.watchLocked(origin, flight)
	}
	go f.run(ctx, flight, slot, allowed)
	return flight
}

func (f *medFetcher) watchLocked(origin string, flight *medFlight) {
	set := f.byOrigin[origin]
	if set == nil {
		set = map[*medFlight]bool{}
		f.byOrigin[origin] = set
	}
	set[flight] = true
	flight.origins = append(flight.origins, origin)
}

func (f *medFetcher) unwatchLocked(flight *medFlight) {
	for _, origin := range flight.origins {
		delete(f.byOrigin[origin], flight)
		if len(f.byOrigin[origin]) == 0 {
			delete(f.byOrigin, origin)
		}
	}
	flight.origins = nil
}

// cancelOrigin cancels the fetches that contact origin and keeps later callers from joining them.
func (f *medFetcher) cancelOrigin(origin string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for flight := range f.byOrigin[origin] {
		flight.cancel()
		if f.flights[flight.key] == flight {
			delete(f.flights, flight.key)
		}
	}
}

func (f *medFetcher) run(ctx context.Context, flight *medFlight, slot wire.MediaReference, allowed func(origin string) bool) {
	defer flight.cancel()
	media, err := f.fetchLimited(ctx, slot, func(origin string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !allowed(origin) {
			flight.moved = origin
			return false
		}
		flight.touched = append(flight.touched, origin)
		f.watchLocked(origin, flight)
		return true
	})
	f.mu.Lock()
	if f.flights[flight.key] == flight {
		delete(f.flights, flight.key)
	}
	f.unwatchLocked(flight)
	f.mu.Unlock()
	if err == nil && ctx.Err() == nil {
		f.cache.put(medKey{sha: slot.SHA256, size: slot.SizeBytes}, media)
	}
	flight.media, flight.err = media, err
	close(flight.done)
}

func (f *medFetcher) fetchLimited(ctx context.Context, slot wire.MediaReference, allowed func(origin string) bool) (content.VerifiedMedia, error) {
	select {
	case f.slots <- struct{}{}:
	case <-ctx.Done():
		return content.VerifiedMedia{}, ctx.Err()
	}
	defer func() { <-f.slots }()
	return medFetch(ctx, slot, allowed)
}

// medStatusError reports an upstream answer that is not a complete object.
type medStatusError struct {
	code int
}

func (e *medStatusError) Error() string {
	return fmt.Sprintf("upstream answered HTTP %d", e.code)
}

// medFetch runs the media pipeline: the signed URL is checked, never repaired, then
// the size is prechecked, the response headers are checked and the bytes are verified.
// Production uses an empty destination allowlist, so private and loopback addresses are never contacted.
func medFetch(ctx context.Context, slot wire.MediaReference, allowed func(origin string) bool) (content.VerifiedMedia, error) {
	budget := content.DefaultMediaBudget()
	if err := content.ValidateExternalURL(slot.URL); err != nil {
		return content.VerifiedMedia{}, err
	}
	if err := content.PrecheckMediaSize(slot, budget); err != nil {
		return content.VerifiedMedia{}, err
	}
	request, err := content.NewFetchRequest(ctx, slot.URL)
	if err != nil {
		return content.VerifiedMedia{}, err
	}
	config := content.FetchConfig{AllowRedirectOrigin: allowed}
	if medFetchOverride != nil {
		medFetchOverride(&config)
	}
	response, err := content.NewFetchClient(config).Do(request)
	if err != nil {
		return content.VerifiedMedia{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return content.VerifiedMedia{}, &medStatusError{code: response.StatusCode}
	}
	if err := content.CheckMediaResponse(response, slot, budget); err != nil {
		return content.VerifiedMedia{}, err
	}
	return content.VerifyMedia(response.Body, slot, budget)
}
