package sdk

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
)

const nmTestAuthor = "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"

func nmTestFixture(t *testing.T) (names.NameRecord, names.NameRecord) {
	t.Helper()
	data, err := os.ReadFile("testdata/nm/name-record.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Record     names.NameRecord `json:"record"`
		Withdrawal names.NameRecord `json:"withdrawal"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture.Record, fixture.Withdrawal
}

func nmTestRecord(t *testing.T, seedHex string, label string, at time.Time) names.NameRecord {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	record, err := names.Build(key, &label, nil, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func nmTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	return nmTestClientWithTransport(t, handler, nil)
}

func nmTestClientWithTransport(t *testing.T, handler http.HandlerFunc, transport http.RoundTripper) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	options := Options{AllowLoopbackHTTP: true, Now: func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }}
	if transport != nil {
		options.HTTPClient = &http.Client{Transport: transport}
	}
	client := rdTestClient(t, server.URL, options)
	return client, server
}

type nmRecordingTransport struct {
	mu    sync.Mutex
	hosts map[string]int
}

func (t *nmRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	if t.hosts == nil {
		t.hosts = map[string]int{}
	}
	t.hosts[request.URL.Hostname()]++
	t.mu.Unlock()
	return http.DefaultTransport.RoundTrip(request)
}

func (t *nmRecordingTransport) requests(host string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hosts[host]
}

func nmTestWriteJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func TestPublishName(t *testing.T) {
	record, _ := nmTestFixture(t)
	body, err := names.Encode(record)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("created sends canonical bytes", func(t *testing.T) {
		client, server := nmTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			got, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || r.URL.Path != "/v1/names" || r.Header.Get("Content-Type") != "application/json" || string(got) != string(body) {
				t.Errorf("request=(%s %s %s %s)", r.Method, r.URL.Path, r.Header.Get("Content-Type"), got)
			}
			nmTestWriteJSON(w, http.StatusCreated, body)
		})
		defer server.Close()
		got, err := client.PublishName(context.Background(), record)
		if err != nil || got != NamePublished {
			t.Fatalf("PublishName=(%v,%v); want published", got, err)
		}
	})
	t.Run("identical current record", func(t *testing.T) {
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, http.StatusOK, body) })
		defer server.Close()
		got, err := client.PublishName(context.Background(), record)
		if err != nil || got != NameAlreadyHeld {
			t.Fatalf("PublishName=(%v,%v); want already held", got, err)
		}
	})
	t.Run("different response is protocol error", func(t *testing.T) {
		_, withdrawal := nmTestFixture(t)
		other, _ := names.Encode(withdrawal)
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, http.StatusCreated, other) })
		defer server.Close()
		_, err := client.PublishName(context.Background(), record)
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(%v)=%s; want protocol", err, Classify(err))
		}
	})
	t.Run("public statuses are preserved", func(t *testing.T) {
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, http.StatusConflict, []byte(`{"status":"not_ready"}`))
		})
		defer server.Close()
		_, err := client.PublishName(context.Background(), record)
		var responseError *Error
		if !errors.As(err, &responseError) || responseError.Status != "not_ready" {
			t.Fatalf("error=%v; want *Error not_ready", err)
		}
	})
	t.Run("rate limit retry delay", func(t *testing.T) {
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, http.StatusTooManyRequests, []byte(`{"status":"rate_limited","retry_after_seconds":3600}`))
		})
		defer server.Close()
		_, err := client.PublishName(context.Background(), record)
		var responseError *Error
		if !errors.As(err, &responseError) || responseError.RetryAfter != time.Hour {
			t.Fatalf("error=%v; want one-hour RetryAfter", err)
		}
	})
	t.Run("invalid record is rejected locally", func(t *testing.T) {
		var requests atomic.Int32
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); nmTestWriteJSON(w, 201, body) })
		defer server.Close()
		invalid := record
		changed := "Changed name"
		invalid.DisplayName = &changed
		if _, err := client.PublishName(context.Background(), invalid); !errors.Is(err, names.ErrIDMismatch) || requests.Load() != 0 {
			t.Fatalf("invalid PublishName error=%v requests=%d", err, requests.Load())
		}
	})
	t.Run("503 is returned unchanged", func(t *testing.T) {
		var requests atomic.Int32
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			nmTestWriteJSON(w, http.StatusServiceUnavailable, []byte(`{"status":"unavailable","retry_after_seconds":30}`))
		})
		defer server.Close()
		_, err := client.PublishName(context.Background(), record)
		var responseError *Error
		if !errors.As(err, &responseError) || responseError.Status != "unavailable" || !IsAmbiguous(err) || requests.Load() != 1 {
			t.Fatalf("error=%v requests=%d; want one unchanged ambiguous unavailable error", err, requests.Load())
		}
	})
}

func TestName(t *testing.T) {
	record, withdrawal := nmTestFixture(t)
	withdrawalBytes, _ := names.Encode(withdrawal)
	t.Run("escaped path and withdrawal", func(t *testing.T) {
		client, server := nmTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.EscapedPath() != "/v1/names/did%3Akey%3Az6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd" {
				t.Errorf("path=%q", r.URL.EscapedPath())
			}
			nmTestWriteJSON(w, http.StatusOK, withdrawalBytes)
		})
		defer server.Close()
		got, err := client.Name(context.Background(), nmTestAuthor)
		if err != nil || got.DisplayName != nil {
			t.Fatalf("Name=(%+v,%v); want withdrawal", got, err)
		}
	})
	t.Run("rejects record for another author", func(t *testing.T) {
		other := nmTestRecord(t, "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "ZO\u00CB", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		data, _ := names.Encode(other)
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, data) })
		defer server.Close()
		_, err := client.Name(context.Background(), nmTestAuthor)
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(%v)=%s; want protocol", err, Classify(err))
		}
	})
	t.Run("rejects broken signature", func(t *testing.T) {
		bad := record
		bad.AuthorSignature = strings.Repeat("A", 86)
		data, _ := names.Encode(bad)
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, data) })
		defer server.Close()
		_, err := client.Name(context.Background(), nmTestAuthor)
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(%v)=%s; want protocol", err, Classify(err))
		}
	})
	t.Run("404 is holder-local not found", func(t *testing.T) {
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, 404, []byte(`{"status":"not_found"}`))
		})
		defer server.Close()
		_, err := client.Name(context.Background(), nmTestAuthor)
		if Classify(err) != SignalNotFound {
			t.Fatalf("Classify(%v)=%s; want not found", err, Classify(err))
		}
	})
	t.Run("invalid DID fails locally", func(t *testing.T) {
		client, server := nmTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
		defer server.Close()
		if _, err := client.Name(context.Background(), "did:key:bad"); !errors.Is(err, discovery.ErrInvalidQuery) {
			t.Fatalf("Name error=%v; want discovery.ErrInvalidQuery", err)
		}
	})
}

func TestNameLookup(t *testing.T) {
	nameA := nmTestRecord(t, "0000000000000000000000000000000000000000000000000000000000000000", "ZO\u00CB", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	nameB := nmTestRecord(t, "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f", "ZO\u00CB", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	if nameA.AuthorID > nameB.AuthorID {
		nameA, nameB = nameB, nameA
	}
	cursor := strings.Repeat("A", 43)
	page1, _ := names.EncodePage(names.NamesPage{Names: []names.NameRecord{nameA}, NextCursor: &cursor})
	page2, _ := names.EncodePage(names.NamesPage{Names: []names.NameRecord{nameB}})
	t.Run("pages normalize query and finish on null", func(t *testing.T) {
		var calls atomic.Int32
		client, server := nmTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			wantQuery := "display_name=ZO%C3%8B"
			if calls.Load() > 0 {
				wantQuery = "cursor=" + cursor + "&display_name=ZO%C3%8B"
			}
			if r.URL.RawQuery != wantQuery {
				t.Errorf("query=%q", r.URL.RawQuery)
			}
			if calls.Add(1) == 1 {
				nmTestWriteJSON(w, 200, page1)
			} else {
				nmTestWriteJSON(w, 200, page2)
			}
		})
		defer server.Close()
		lookup, err := NewNameLookup(client, "ZOE\u0308")
		if err != nil {
			t.Fatal(err)
		}
		first, err := lookup.Next(context.Background())
		if err != nil || first.Done || len(first.Records) != 1 {
			t.Fatalf("first=(%+v,%v)", first, err)
		}
		second, err := lookup.Next(context.Background())
		if err != nil || !second.Done || len(second.Records) != 1 || first.Records[0].AuthorID != nameA.AuthorID || second.Records[0].AuthorID != nameB.AuthorID {
			t.Fatalf("second=(%+v,%v)", second, err)
		}
		last, err := lookup.Next(context.Background())
		if err != nil || !last.Done {
			t.Fatalf("last=(%+v,%v)", last, err)
		}
	})
	t.Run("rejects a nonmatching name", func(t *testing.T) {
		bad, _ := names.EncodePage(names.NamesPage{Names: []names.NameRecord{nmTestRecord(t, "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f", "Bob", time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))}})
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, bad) })
		defer server.Close()
		lookup, _ := NewNameLookup(client, "Ada")
		if _, err := lookup.Next(context.Background()); Classify(err) != SignalProtocol {
			t.Fatalf("Classify(%v)=%s", err, Classify(err))
		}
	})
	t.Run("invalid cursor loses snapshot", func(t *testing.T) {
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, 400, []byte(`{"status":"invalid_cursor"}`))
		})
		defer server.Close()
		lookup, _ := NewNameLookup(client, "Ada")
		if _, err := lookup.Next(context.Background()); !errors.Is(err, ErrSnapshotLost) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("rejects a cursor returned twice", func(t *testing.T) {
		looping, _ := names.EncodePage(names.NamesPage{Names: []names.NameRecord{nameA}, NextCursor: &cursor})
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, looping) })
		defer server.Close()
		lookup, _ := NewNameLookup(client, "ZO\u00CB")
		if _, err := lookup.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := lookup.Next(context.Background()); Classify(err) != SignalProtocol {
			t.Fatalf("Classify(%v)=%s", err, Classify(err))
		}
	})
	t.Run("second page repeats an author_id", func(t *testing.T) {
		repeated, _ := names.EncodePage(names.NamesPage{Names: []names.NameRecord{nameA}})
		var calls atomic.Int32
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				nmTestWriteJSON(w, 200, page1)
				return
			}
			nmTestWriteJSON(w, 200, repeated)
		})
		defer server.Close()
		lookup, _ := NewNameLookup(client, "ZO\u00CB")
		if _, err := lookup.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := lookup.Next(context.Background()); Classify(err) != SignalProtocol {
			t.Fatalf("Classify(%v)=%s; want protocol", err, Classify(err))
		}
		if _, err := lookup.Next(context.Background()); Classify(err) != SignalProtocol {
			t.Fatalf("after protocol error Classify(%v)=%s; want protocol", err, Classify(err))
		}
	})
	t.Run("rejects descending author order", func(t *testing.T) {
		page, _ := names.EncodePage(names.NamesPage{Names: []names.NameRecord{nameB, nameA}})
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, page) })
		defer server.Close()
		lookup, _ := NewNameLookup(client, "ZO\u00CB")
		if _, err := lookup.Next(context.Background()); Classify(err) != SignalProtocol {
			t.Fatalf("Classify(%v)=%s; want protocol", err, Classify(err))
		}
	})
	t.Run("rejects more than fifty records", func(t *testing.T) {
		encoded, _ := names.Encode(nameA)
		body := []byte(`{"names":[` + strings.TrimSuffix(strings.Repeat(string(encoded)+",", 51), ",") + `],"next_cursor":null}`)
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, body) })
		defer server.Close()
		lookup, _ := NewNameLookup(client, "ZO\u00CB")
		if _, err := lookup.Next(context.Background()); Classify(err) != SignalProtocol {
			t.Fatalf("Classify(%v)=%s", err, Classify(err))
		}
	})
}

func TestResolveName(t *testing.T) {
	record, withdrawal := nmTestFixture(t)
	withdrawalBytes, _ := names.Encode(withdrawal)
	recordBytes, _ := names.Encode(record)
	t.Run("selects newest verified holder record", func(t *testing.T) {
		clientA, serverA := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, 200, recordBytes)
		})
		defer serverA.Close()
		clientB, serverB := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, 200, withdrawalBytes)
		})
		defer serverB.Close()
		clientC, serverC := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, 404, []byte(`{"status":"not_found"}`))
		})
		defer serverC.Close()
		clientD, serverD := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, 503, []byte(`{"status":"unavailable","retry_after_seconds":30}`))
		})
		defer serverD.Close()
		view := ResolveName(context.Background(), []*Client{clientA, clientB, clientC, clientD}, nmTestAuthor, ResolveNameOptions{})
		if !view.Found || !view.Withdrawn || view.Record.NameID != withdrawal.NameID || !view.Sources[0].Behind || view.Fingerprint != names.Fingerprint(nmTestAuthor) {
			t.Fatalf("view=%+v", view)
		}
		if view.Sources[1].Behind || view.Sources[2].Signal != SignalNotFound || view.Sources[3].Signal != SignalUnavailable || view.Sources[2].NameID != "" {
			t.Fatalf("signals=%+v", view.Sources)
		}
	})
	t.Run("preserves avatar reference without fetching image", func(t *testing.T) {
		transport := &nmRecordingTransport{}
		client, server := nmTestClientWithTransport(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, recordBytes) }, transport)
		defer server.Close()
		view := ResolveName(context.Background(), []*Client{client}, nmTestAuthor, ResolveNameOptions{})
		if !view.Found || view.Record.Avatar == nil || view.Record.Avatar.URL != "https://media.example/avatars/zoe.png" {
			t.Fatalf("view=%+v; want verified avatar reference preserved", view)
		}
		if transport.requests("127.0.0.1") != 1 || transport.requests("media.example") != 0 {
			t.Fatalf("holder requests=%d avatar host requests=%d; want 1 and 0", transport.requests("127.0.0.1"), transport.requests("media.example"))
		}
	})
	t.Run("404 does not hide valid holder record", func(t *testing.T) {
		clientA, serverA := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, recordBytes) })
		defer serverA.Close()
		clientC, serverC := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, 404, []byte(`{"status":"not_found"}`))
		})
		defer serverC.Close()
		view := ResolveName(context.Background(), []*Client{clientA, clientC}, nmTestAuthor, ResolveNameOptions{})
		if !view.Found || view.Record.NameID != record.NameID || view.Sources[1].Signal != SignalNotFound {
			t.Fatalf("view=%+v", view)
		}
	})
	t.Run("all not found returns no record", func(t *testing.T) {
		client, server := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			nmTestWriteJSON(w, 404, []byte(`{"status":"not_found"}`))
		})
		defer server.Close()
		view := ResolveName(context.Background(), []*Client{client}, nmTestAuthor, ResolveNameOptions{})
		if view.Found {
			t.Fatalf("view=%+v", view)
		}
	})
	t.Run("forged later record does not affect selection", func(t *testing.T) {
		forged := record
		forged.IssuedAt = "2026-11-12T00:00:00Z"
		forgedBytes, _ := names.Encode(forged)
		clientA, serverA := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, recordBytes) })
		defer serverA.Close()
		clientB, serverB := nmTestClient(t, func(w http.ResponseWriter, _ *http.Request) { nmTestWriteJSON(w, 200, forgedBytes) })
		defer serverB.Close()
		view := ResolveName(context.Background(), []*Client{clientA, clientB}, nmTestAuthor, ResolveNameOptions{})
		if !view.Found || view.Record.NameID != record.NameID || view.Sources[1].Signal != SignalProtocol || view.Sources[1].NameID != "" {
			t.Fatalf("view=%+v", view)
		}
	})
}
