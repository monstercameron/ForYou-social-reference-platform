package provider

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ReadClass identifies the public request work being limited.
type ReadClass int

const (
	// ReadDirect identifies a direct message read.
	ReadDirect ReadClass = iota + 1
	// ReadListing identifies a listing, index, or changes read.
	ReadListing
	// ReadOther identifies another unauthenticated route.
	ReadOther
)

type bucket struct {
	tokens float64
	at     time.Time
}
type sourceBuckets struct {
	buckets map[ReadClass]*bucket
	used    uint64
}
type limiter struct {
	mu      sync.Mutex
	cfg     ReadLimits
	now     func() time.Time
	totals  map[ReadClass]*bucket
	sources map[string]*sourceBuckets
	used    uint64
}

func defaultReadLimits() ReadLimits { return ReadLimits{600, 30, 60, 6, 64, 4096} }
func (l *ReadLimits) validate() error {
	d := defaultReadLimits()
	if l.DirectPerMinute == 0 {
		l.DirectPerMinute = d.DirectPerMinute
	}
	if l.DirectPerSourcePerMinute == 0 {
		l.DirectPerSourcePerMinute = d.DirectPerSourcePerMinute
	}
	if l.ListingPerMinute == 0 {
		l.ListingPerMinute = d.ListingPerMinute
	}
	if l.ListingPerSourcePerMinute == 0 {
		l.ListingPerSourcePerMinute = d.ListingPerSourcePerMinute
	}
	if l.MaxConcurrent == 0 {
		l.MaxConcurrent = d.MaxConcurrent
	}
	if l.MaxSources == 0 {
		l.MaxSources = d.MaxSources
	}
	if l.DirectPerMinute < 600 {
		return invalidField("Reads.DirectPerMinute", "below floor 600")
	}
	if l.DirectPerSourcePerMinute < 30 {
		return invalidField("Reads.DirectPerSourcePerMinute", "below floor 30")
	}
	if l.ListingPerMinute < 60 {
		return invalidField("Reads.ListingPerMinute", "below floor 60")
	}
	if l.ListingPerSourcePerMinute < 6 {
		return invalidField("Reads.ListingPerSourcePerMinute", "below floor 6")
	}
	if l.MaxConcurrent < 1 {
		return invalidField("Reads.MaxConcurrent", "must be positive")
	}
	if l.MaxSources < 1 {
		return invalidField("Reads.MaxSources", "must be positive")
	}
	return nil
}
func newLimiter(c ReadLimits, now func() time.Time) *limiter {
	return &limiter{cfg: c, now: now, totals: map[ReadClass]*bucket{}, sources: map[string]*sourceBuckets{}}
}

// AllowRead applies aggregate and per-source continuous-refill token buckets.
func (s *Service) AllowRead(class ReadClass, r *http.Request) (int, bool) {
	return s.reads.allow(class, r)
}

// LimitRead applies the configured read buckets and writes the standard 429 response when denied.
func (s *Service) LimitRead(w http.ResponseWriter, r *http.Request, class ReadClass) bool {
	retry, ok := s.AllowRead(class, r)
	if !ok {
		writeRetry(w, http.StatusTooManyRequests, "rate_limited", retry)
	}
	return ok
}

func (l *limiter) allow(class ReadClass, r *http.Request) (int, bool) {
	if class != ReadDirect && class != ReadListing && class != ReadOther {
		class = ReadOther
	}
	per, source := l.cfg.ListingPerMinute, l.cfg.ListingPerSourcePerMinute
	if class == ReadDirect {
		per, source = l.cfg.DirectPerMinute, l.cfg.DirectPerSourcePerMinute
	}
	key := requestSource(r.RemoteAddr)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	total := l.totals[class]
	if total == nil {
		total = &bucket{tokens: float64(per), at: now}
		l.totals[class] = total
	}
	sourceEntry := l.sources[key]
	if sourceEntry == nil {
		if len(l.sources) >= l.cfg.MaxSources {
			var oldest string
			var stamp uint64 = ^uint64(0)
			for k, entry := range l.sources {
				if entry.used < stamp {
					oldest, stamp = k, entry.used
				}
			}
			delete(l.sources, oldest)
		}
		sourceEntry = &sourceBuckets{buckets: map[ReadClass]*bucket{}}
		l.sources[key] = sourceEntry
	}
	l.used++
	sourceEntry.used = l.used
	src := sourceEntry.buckets[class]
	if src == nil {
		src = &bucket{tokens: float64(source), at: now}
		sourceEntry.buckets[class] = src
	}
	refill := func(b *bucket, capacity int) {
		elapsed := now.Sub(b.at).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * float64(capacity) / 60
			if b.tokens > float64(capacity) {
				b.tokens = float64(capacity)
			}
			b.at = now
		}
	}
	refill(total, per)
	refill(src, source)
	wait := func(b *bucket, capacity int) int {
		n := int(math.Ceil((1-b.tokens)*60/float64(capacity) - 1e-9))
		if n < 1 {
			n = 1
		}
		return n
	}
	if total.tokens < 1 || src.tokens < 1 {
		retry := wait(total, per)
		if n := wait(src, source); n > retry {
			retry = n
		}
		return retry, false
	}
	total.tokens--
	src.tokens--
	return 0, true
}

func requestSource(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}
