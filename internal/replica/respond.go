package replica

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/apierr"
)

// ReadClass identifies a rate-limited anonymous read request class.
type ReadClass int

const (
	// ReadDirect covers direct message reads.
	ReadDirect ReadClass = iota + 1
	// ReadListing covers listing and changes requests.
	ReadListing
)

type bucket struct {
	tokens float64
	last   time.Time
	used   time.Time
}

type readLimiter struct {
	mu      sync.Mutex
	limits  ReadLimits
	now     func() time.Time
	global  map[ReadClass]*bucket
	sources map[ReadClass]map[string]*bucket
}

func newReadLimiter(limits ReadLimits, now func() time.Time) *readLimiter {
	return &readLimiter{limits: limits, now: now, global: make(map[ReadClass]*bucket), sources: make(map[ReadClass]map[string]*bucket)}
}

func (l *readLimiter) rates(class ReadClass) (int, int, bool) {
	switch class {
	case ReadDirect:
		return l.limits.DirectPerMinute, l.limits.DirectPerSourcePerMinute, true
	case ReadListing:
		return l.limits.ListingPerMinute, l.limits.ListingPerSourcePerMinute, true
	default:
		return 0, 0, false
	}
}

// AllowRead applies aggregate and peer-address token buckets to one request.
func (s *Service) AllowRead(class ReadClass, r *http.Request) (retryAfter int, ok bool) {
	globalRate, sourceRate, valid := s.limiter.rates(class)
	if !valid || r == nil {
		return 1, false
	}
	now := s.Now()
	source := peerKey(r.RemoteAddr)
	s.limiter.mu.Lock()
	defer s.limiter.mu.Unlock()
	global := s.limiter.global[class]
	if global == nil {
		global = newBucket(globalRate, now)
		s.limiter.global[class] = global
	}
	if s.limiter.sources[class] == nil {
		s.limiter.sources[class] = make(map[string]*bucket)
	}
	sources := s.limiter.sources[class]
	perSource := sources[source]
	if perSource == nil {
		if len(sources) >= s.cfg.Reads.MaxSources {
			evictLeastRecentlyUsed(sources)
		}
		perSource = newBucket(sourceRate, now)
		sources[source] = perSource
	}
	refill(global, globalRate, now)
	refill(perSource, sourceRate, now)
	globalWait := retryWait(global, globalRate)
	sourceWait := retryWait(perSource, sourceRate)
	if globalWait > 0 || sourceWait > 0 {
		if globalWait > sourceWait {
			return globalWait, false
		}
		return sourceWait, false
	}
	global.tokens--
	perSource.tokens--
	return 0, true
}

func newBucket(rate int, now time.Time) *bucket {
	return &bucket{tokens: float64(rate), last: now, used: now}
}

func refill(b *bucket, rate int, now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(float64(rate), b.tokens+elapsed*float64(rate)/60)
	}
	b.last = now
	b.used = now
}

func retryWait(b *bucket, rate int) int {
	if b.tokens >= 1 {
		return 0
	}
	return max(1, int(math.Ceil((1-b.tokens)*60/float64(rate))))
}

func evictLeastRecentlyUsed(sources map[string]*bucket) {
	oldestKey := ""
	var oldest time.Time
	for key, entry := range sources {
		if oldestKey == "" || entry.used.Before(oldest) {
			oldestKey, oldest = key, entry.used
		}
	}
	delete(sources, oldestKey)
}

func peerKey(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return "invalid"
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

func (s *Service) addHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeCanonical(w, http.StatusOK, []byte(`{"role":"replica","status":"ok"}`), "no-store")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if s.isOpen() {
			writeCanonical(w, http.StatusOK, []byte(`{"role":"replica","status":"ready"}`), "no-store")
			return
		}
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
	})
}

func (s *Service) isOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}

func writeError(w http.ResponseWriter, status int, name string) {
	publicStatus := apierr.Status(name)
	code, mapped := publicStatus.HTTPStatus()
	if !mapped || code != status {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.Unavailable, RetryAfterSeconds: apierr.DefaultRetryAfterSeconds})
		return
	}
	apierr.Write(w, apierr.PublicReadError{Status: publicStatus})
}

func writeRetry(w http.ResponseWriter, status int, name string, seconds int) {
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 86400 {
		seconds = 86400
	}
	publicStatus := apierr.Status(name)
	code, mapped := publicStatus.HTTPStatus()
	if !mapped || code != status || status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.Unavailable, RetryAfterSeconds: apierr.DefaultRetryAfterSeconds})
		return
	}
	apierr.Write(w, apierr.PublicReadError{Status: publicStatus, RetryAfterSeconds: seconds})
}

func writeCanonical(w http.ResponseWriter, status int, body []byte, cacheControl string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", cacheControl)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
