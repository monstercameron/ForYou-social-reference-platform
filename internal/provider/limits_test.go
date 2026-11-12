package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLimitsPerSourceAndIPv6Prefix(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	request := func(remote string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		_, ok := s.AllowRead(ReadListing, r)
		return ok
	}
	if !request("192.0.2.1:1000") || !request("192.0.2.1:1001") || !request("192.0.2.1:1002") || !request("192.0.2.1:1003") || !request("192.0.2.1:1004") || !request("192.0.2.1:1005") {
		t.Fatal("first six source requests should pass")
	}
	if retry, ok := s.AllowRead(ReadListing, listingRequest("192.0.2.1:1006")); ok || retry != 10 {
		t.Fatalf("seventh request = (%d, %t)", retry, ok)
	}
	if retry, ok := s.AllowRead(ReadListing, listingRequest("192.0.2.2:1000")); !ok || retry != 0 {
		t.Fatalf("other source = (%d, %t)", retry, ok)
	}
	if !request("[2001:db8::1]:1000") {
		t.Fatal("first IPv6 request denied")
	}
	if !request("[2001:db8::1]:1001") || !request("[2001:db8::1]:1002") || !request("[2001:db8::1]:1003") || !request("[2001:db8::1]:1004") || !request("[2001:db8::1]:1005") {
		t.Fatal("IPv6 source exhausted before its per-source limit")
	}
	if retry, ok := s.AllowRead(ReadListing, listingRequest("[2001:db8::2]:1000")); ok || retry < 1 {
		t.Fatalf("same /64 = (%d, %t)", retry, ok)
	}
}

func TestLimitReadWritesRateLimitResponse(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := listingRequest("192.0.2.9:1000")
	if !s.LimitRead(httptest.NewRecorder(), r, ReadListing) || !s.LimitRead(httptest.NewRecorder(), r, ReadListing) || !s.LimitRead(httptest.NewRecorder(), r, ReadListing) || !s.LimitRead(httptest.NewRecorder(), r, ReadListing) || !s.LimitRead(httptest.NewRecorder(), r, ReadListing) || !s.LimitRead(httptest.NewRecorder(), r, ReadListing) {
		t.Fatal("first six listing requests were denied")
	}
	w := httptest.NewRecorder()
	if s.LimitRead(w, r, ReadListing) {
		t.Fatal("seventh listing request was allowed")
	}
	if w.Code != http.StatusTooManyRequests || w.Body.String() != `{"retry_after_seconds":10,"status":"rate_limited"}` || w.Header().Get("Retry-After") != "10" {
		t.Fatalf("limited response = %d %q %v", w.Code, w.Body.String(), w.Header())
	}
}

func TestDecodeJSONStrict(t *testing.T) {
	t.Run("duplicate keys", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1,"a":2}`))
		var v struct {
			A int `json:"a"`
		}
		if _, err := decodeJSON(r, 100, &v); !errors.Is(err, errBadJSON) {
			t.Fatal("duplicate key accepted")
		}
	})
	t.Run("unknown fields", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1,"x":2}`))
		var v struct {
			A int `json:"a"`
		}
		if _, err := decodeJSON(r, 100, &v); !errors.Is(err, errBadJSON) {
			t.Fatal("unknown field accepted")
		}
	})
	t.Run("oversize body", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`12345`))
		var v any
		if _, err := decodeJSON(r, 4, &v); err != errBodyTooLarge {
			t.Fatalf("decode error = %v", err)
		}
	})
	t.Run("nesting deeper than 32", func(t *testing.T) {
		body := strings.Repeat("[", 33) + "0" + strings.Repeat("]", 33)
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		var v any
		if _, err := decodeJSON(r, 100, &v); !errors.Is(err, errBadJSON) {
			t.Fatalf("decode error = %v", err)
		}
	})
}

func TestReadFloorRejected(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Reads.ListingPerSourcePerMinute = 5
	if _, err = New(cfg); err == nil {
		t.Fatal("below-floor rate accepted")
	}
}

func TestSettlementReadsHaveTheirOwnBuckets(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spend := func(class ReadClass, times int) bool {
		allowed := true
		for range times {
			_, allowed = s.AllowRead(class, listingRequest("203.0.113.9:4000"))
		}
		return allowed
	}
	t.Run("a monitor's sixty claim reads in a minute all pass", func(t *testing.T) {
		if !spend(ReadSettlement, 60) {
			t.Error("the 60th settlement read from one source was refused; the floor is 60 a minute")
		}
	})
	t.Run("the sixty-first is refused", func(t *testing.T) {
		if spend(ReadSettlement, 1) {
			t.Error("the 61st settlement read in the same minute was allowed")
		}
	})
	t.Run("spent settlement reads leave the listing allowance whole", func(t *testing.T) {
		if !spend(ReadListing, 6) {
			t.Error("six listing reads were refused after the settlement bucket ran dry")
		}
	})
	t.Run("a settlement floor below the specification is rejected", func(t *testing.T) {
		low := cfg
		low.Reads.SettlementPerSourcePerMinute = 59
		if _, err := New(low); err == nil {
			t.Error("a per-source settlement rate of 59 was accepted; the floor is 60")
		}
	})
}

func listingRequest(remote string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	return r
}
