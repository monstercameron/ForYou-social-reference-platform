package server

import (
	"context"
	"sort"
	"sync"
	"time"

	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/sdk"
)

const (
	namesCacheLimit   = 10000
	namesRefreshAge   = 10 * time.Minute
	namesWantedWindow = 24 * time.Hour
	namesRefreshBatch = 100
)

var namesCaches sync.Map

func init() { backgroundJobs = append(backgroundJobs, refreshNames) }

// nameCache holds verified public name views keyed by author key. It never holds
// reader state: petnames live in their own per-profile file. Rendering only reads
// it and marks the author as wanted; providers are asked by refreshNames and by the
// reader's own name checks. Those requests tell the providers which keys this
// client displays.
type nameCache struct {
	mu      sync.Mutex
	entries map[string]*nameEntry
	folds   map[string]map[string]bool
}

type nameEntry struct {
	view    sdk.NameView
	known   bool
	fetched time.Time
	wanted  time.Time
	fold    string
}

func newNameCache() *nameCache {
	return &nameCache{entries: map[string]*nameEntry{}, folds: map[string]map[string]bool{}}
}

// nameCache returns the cache of this App, created on first use.
func (a *App) nameCache() *nameCache {
	cache, _ := namesCaches.LoadOrStore(a, newNameCache())
	return cache.(*nameCache)
}

// want marks author as wanted at now and returns its cached view and whether one was fetched.
func (c *nameCache) want(author string, now time.Time) (sdk.NameView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entryLocked(author, now)
	entry.wanted = now
	return entry.view, entry.known
}

// store replaces the cached view of author with a view fetched at now.
func (c *nameCache) store(author string, view sdk.NameView, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entryLocked(author, now)
	c.unindexLocked(author, entry)
	entry.view, entry.known, entry.fetched = view, true, now
	if name, ok := currentName(view); ok {
		entry.fold = names.FoldKey(name)
		if c.folds[entry.fold] == nil {
			c.folds[entry.fold] = map[string]bool{}
		}
		c.folds[entry.fold][author] = true
	}
}

// sharers returns the other cached authors whose current display name matches name.
// Only cached names are ever compared, so two keys on one screen are both cached.
func (c *nameCache) sharers(name, author string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var others []string
	for other := range c.folds[names.FoldKey(name)] {
		if other != author {
			others = append(others, other)
		}
	}
	sort.Strings(others)
	return others
}

// stale returns up to namesRefreshBatch wanted authors whose view is missing or
// older than namesRefreshAge, most recently wanted first. Authors not wanted within
// the window are dropped.
func (c *nameCache) stale(now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	type candidate struct {
		author string
		wanted time.Time
	}
	var list []candidate
	for author, entry := range c.entries {
		if now.Sub(entry.wanted) > namesWantedWindow {
			c.unindexLocked(author, entry)
			delete(c.entries, author)
			continue
		}
		if !entry.known || now.Sub(entry.fetched) > namesRefreshAge {
			list = append(list, candidate{author, entry.wanted})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].wanted.Equal(list[j].wanted) {
			return list[i].wanted.After(list[j].wanted)
		}
		return list[i].author < list[j].author
	})
	if len(list) > namesRefreshBatch {
		list = list[:namesRefreshBatch]
	}
	authors := make([]string, len(list))
	for i, item := range list {
		authors[i] = item.author
	}
	return authors
}

func (c *nameCache) entryLocked(author string, now time.Time) *nameEntry {
	if entry := c.entries[author]; entry != nil {
		return entry
	}
	if len(c.entries) >= namesCacheLimit {
		c.evictLocked()
	}
	entry := &nameEntry{wanted: now}
	c.entries[author] = entry
	return entry
}

func (c *nameCache) evictLocked() {
	var oldest string
	var oldestWanted time.Time
	for author, entry := range c.entries {
		if oldest == "" || entry.wanted.Before(oldestWanted) || (entry.wanted.Equal(oldestWanted) && author < oldest) {
			oldest, oldestWanted = author, entry.wanted
		}
	}
	c.unindexLocked(oldest, c.entries[oldest])
	delete(c.entries, oldest)
}

func (c *nameCache) unindexLocked(author string, entry *nameEntry) {
	if entry == nil || entry.fold == "" {
		return
	}
	delete(c.folds[entry.fold], author)
	if len(c.folds[entry.fold]) == 0 {
		delete(c.folds, entry.fold)
	}
	entry.fold = ""
}

// current returns the cached view of author without marking it wanted.
func (c *nameCache) current(author string) sdk.NameView {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[author]; entry != nil && entry.known {
		return entry.view
	}
	return sdk.NameView{}
}

// currentName returns the display name of a verified view that is not withdrawn.
func currentName(view sdk.NameView) (string, bool) {
	if !view.Found || view.Withdrawn || view.Record.DisplayName == nil {
		return "", false
	}
	return *view.Record.DisplayName, true
}

// namesClients returns the clients the background refresh asks: the configured
// default providers, then the mirrors.
func (a *App) namesClients() []*sdk.Client {
	clients := make([]*sdk.Client, 0, len(a.config.Providers))
	for _, endpoint := range a.config.Providers {
		if client, err := a.providerClient(endpoint); err == nil {
			clients = append(clients, client)
		}
	}
	return append(clients, a.mirrorClients()...)
}

// refreshNames fetches the names of wanted authors that are missing or stale.
func refreshNames(ctx context.Context, a *App) error {
	cache := a.nameCache()
	authors := cache.stale(a.now())
	clients := a.namesClients()
	if len(clients) == 0 {
		return nil
	}
	for _, author := range authors {
		if err := ctx.Err(); err != nil {
			return err
		}
		view := sdk.ResolveName(ctx, clients, author, sdk.ResolveNameOptions{})
		if namesAnswered(view) {
			cache.store(author, view, a.now())
		}
	}
	return nil
}

// namesAnswered reports whether any holder answered with a record or a not-found.
// A view built only from failures never replaces what the cache holds. A record is
// told by its NameID: the zero Signal is also sdk.SignalOther, a failure.
func namesAnswered(view sdk.NameView) bool {
	for _, source := range view.Sources {
		if source.NameID != "" || source.Signal == sdk.SignalNotFound {
			return true
		}
	}
	return false
}
