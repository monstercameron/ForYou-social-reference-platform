package replica

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func readTestService(t *testing.T, now time.Time, public bool) *Service {
	t.Helper()
	return readTestServiceWithClock(t, func() time.Time { return now }, public)
}

func readTestServiceWithClock(t *testing.T, now func() time.Time, public bool) *Service {
	t.Helper()
	cfg := replicaConfig(t, t.TempDir(), now())
	cfg.Now = now
	if public {
		cfg.PublicOrigin = "https://replica.example"
		cfg.Operator = OperatorConfig{LegalContact: "legal@example.invalid", AbuseContact: "abuse@example.invalid", PrivacyContact: "privacy@example.invalid"}
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if added, err := s.Records().Import(keys.DIDOf(devnet.ProviderKey(0)), versionCopies(t), 5); err != nil || added != 5 {
		t.Fatalf("Records().Import() = (%d, %v); want (5, nil)", added, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func readTestRequest(t *testing.T, s *Service, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "192.0.2.10:1234"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestReplicaDirectRead(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s := readTestService(t, now, false)
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	copies := versionCopies(t)
	if copies[0].MessageID != id {
		t.Fatalf("fixture first message ID = %q; want %q", copies[0].MessageID, id)
	}
	want := copies[0].Bytes
	t.Run("GET returns exact bytes with one hour cache", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodGet, "/v1/messages/"+id)
		if response.Code != http.StatusOK || response.Body.String() != string(want) {
			t.Fatalf("GET status/body = %d/%q; want 200 and stored bytes", response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "public, max-age=3600" {
			t.Fatalf("Cache-Control = %q; want public, max-age=3600", got)
		}
		if response.Header().Get("ETag") != "" || response.Header().Get("Last-Modified") != "" {
			t.Fatal("direct response unexpectedly contains a validator")
		}
	})
	t.Run("uppercase escaped colon is accepted", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodGet, "/v1/messages/fy1%3A4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f")
		if response.Code != http.StatusOK || response.Body.String() != string(want) {
			t.Fatalf("escaped path response = %d %q; want fixture bytes", response.Code, response.Body.String())
		}
	})
	t.Run("malformed direct path is nonrevealing and not cached", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodGet, "/v1/messages/fy1:"+strings.Repeat("A", 64))
		if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("malformed response = %d %q Cache-Control=%q", response.Code, response.Body.String(), response.Header().Get("Cache-Control"))
		}
	})
	t.Run("unknown copy is nonrevealing and not cached", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodGet, "/v1/messages/fy1:"+strings.Repeat("0", 64))
		if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unknown response = %d %q Cache-Control=%q", response.Code, response.Body.String(), response.Header().Get("Cache-Control"))
		}
	})
	t.Run("Range is ignored", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/messages/"+id, nil)
		r.RemoteAddr = "192.0.2.10:1234"
		r.Header.Set("Range", "bytes=0-1")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, r)
		if response.Code != http.StatusOK || response.Body.String() != string(want) {
			t.Fatalf("Range response = %d with %d bytes; want full 200 body", response.Code, response.Body.Len())
		}
	})
	t.Run("HEAD has no body and is not cached", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodHead, "/v1/messages/"+id)
		if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("HEAD response = %d body=%d Cache-Control=%q", response.Code, response.Body.Len(), response.Header().Get("Cache-Control"))
		}
	})
	t.Run("unsupported method returns 405 and Allow", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodPost, "/v1/messages/"+id)
		if response.Code != http.StatusMethodNotAllowed || response.Body.Len() != 0 || response.Header().Get("Allow") != "GET, HEAD" || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("POST response = %d %q Allow=%q Cache-Control=%q", response.Code, response.Body.String(), response.Header().Get("Allow"), response.Header().Get("Cache-Control"))
		}
	})
}

func TestReplicaListingAndIndexes(t *testing.T) {
	s := readTestService(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), false)
	t.Run("messages paginate in canonical order to null cursor", func(t *testing.T) {
		first := readTestRequest(t, s, http.MethodGet, "/v1/messages?limit=2")
		var page wire.DiscoveryPage
		if first.Code != http.StatusOK || wire.Decode(first.Body.Bytes(), "DiscoveryPage", &page) != nil {
			t.Fatalf("first page status/body = %d %q", first.Code, first.Body.String())
		}
		if len(page.Messages) != 2 || page.Messages[0].MessageID != "fy1:e703e832f979cb8c1dc232ad719a4f09117617c7852774121e90b4ca2e345262" || page.Messages[1].MessageID != "fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf" || page.NextCursor == nil {
			t.Fatalf("first page IDs/cursor = %v/%v", page.Messages, page.NextCursor)
		}
		cursor := *page.NextCursor
		second := readTestRequest(t, s, http.MethodGet, "/v1/messages?limit=2&cursor="+cursor)
		if second.Code != http.StatusOK || wire.Decode(second.Body.Bytes(), "DiscoveryPage", &page) != nil {
			t.Fatalf("second page status/body = %d %q", second.Code, second.Body.String())
		}
		if len(page.Messages) != 2 || page.Messages[0].MessageID != "fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b" || page.Messages[1].MessageID != "fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72" || page.NextCursor == nil {
			t.Fatalf("second page IDs/cursor = %v/%v", page.Messages, page.NextCursor)
		}
		third := readTestRequest(t, s, http.MethodGet, "/v1/messages?limit=2&cursor="+*page.NextCursor)
		if third.Code != http.StatusOK || wire.Decode(third.Body.Bytes(), "DiscoveryPage", &page) != nil {
			t.Fatalf("third page status/body = %d %q", third.Code, third.Body.String())
		}
		if len(page.Messages) != 1 || page.Messages[0].MessageID != "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f" || page.NextCursor != nil {
			t.Fatalf("third page IDs/cursor = %v/%v; want final ID and null", page.Messages, page.NextCursor)
		}
		if first.Header().Get("Cache-Control") != "no-store" || second.Header().Get("Cache-Control") != "no-store" || third.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("listing page was cacheable")
		}
	})
	t.Run("immediate successors use encoded message target", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodGet, "/v1/versions/fy1%3A4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f?limit=100")
		var page wire.DiscoveryPage
		if response.Code != http.StatusOK || wire.Decode(response.Body.Bytes(), "DiscoveryPage", &page) != nil {
			t.Fatalf("version response = %d %q", response.Code, response.Body.String())
		}
		if len(page.Messages) != 2 || page.Messages[0].MessageID != "fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf" || page.Messages[1].MessageID != "fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72" {
			t.Fatalf("version IDs = %v", page.Messages)
		}
	})
	t.Run("unknown cursor has invalid cursor status", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodGet, "/v1/messages?cursor=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		if response.Code != http.StatusBadRequest || response.Body.String() != `{"status":"invalid_cursor"}` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("bad cursor response = %d %q", response.Code, response.Body.String())
		}
	})
}

func TestReplicaChanges(t *testing.T) {
	s := readTestService(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), false)
	response := readTestRequest(t, s, http.MethodGet, "/v1/changes?after=0&limit=2")
	var page wire.ChangesPage
	if response.Code != http.StatusOK || wire.Decode(response.Body.Bytes(), "ChangesPage", &page) != nil {
		t.Fatalf("changes response = %d %q", response.Code, response.Body.String())
	}
	if len(page.Items) != 2 || page.Items[0].Sequence != 1 || page.Items[1].Sequence != 2 || page.NextAfter != 2 || !page.More || page.ProviderID != s.ReplicaID() || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("changes page = %+v", page)
	}
}

func TestReplicaManifestAndPolicy(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	clockNow := now
	s := readTestServiceWithClock(t, func() time.Time { return clockNow }, true)
	first := readTestRequest(t, s, http.MethodGet, "/.well-known/foryou-operator.json")
	if first.Code != http.StatusOK || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("manifest status/cache = %d/%q", first.Code, first.Header().Get("Cache-Control"))
	}
	var manifest wire.OperatorManifest
	if err := wire.Decode(first.Body.Bytes(), "OperatorManifest", &manifest); err != nil {
		t.Fatalf("manifest schema: %v", err)
	}
	unsigned, err := canon.EncodeWithout(manifest, "signature")
	if err != nil || keys.Verify(s.ReplicaID(), "operator-manifest", unsigned, manifest.Signature) != nil {
		t.Fatalf("manifest signature verification failed: %v", err)
	}
	policy := readTestRequest(t, s, http.MethodGet, "/policy")
	policyDigest := sha256.Sum256(policy.Body.Bytes())
	if policy.Code != http.StatusOK || manifest.PolicyDigest != hex.EncodeToString(policyDigest[:]) || policy.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("policy status/digest/cache = %d/%s/%q", policy.Code, manifest.PolicyDigest, policy.Header().Get("Cache-Control"))
	}
	second := readTestRequest(t, s, http.MethodGet, "/.well-known/foryou-operator.json")
	if second.Body.String() != first.Body.String() {
		t.Fatal("manifest changed without a content change or renewal")
	}
	t.Run("renewal after 24 days increments the version", func(t *testing.T) {
		later := now.Add(24 * 24 * time.Hour)
		clockNow = later
		response := readTestRequest(t, s, http.MethodGet, "/.well-known/foryou-operator.json")
		var renewed wire.OperatorManifest
		if response.Code != http.StatusOK || wire.Decode(response.Body.Bytes(), "OperatorManifest", &renewed) != nil || renewed.ManifestVersion != 2 || renewed.IssuedAt != later.Format("2006-01-02T15:04:05Z") {
			t.Fatalf("renewed manifest = %d %q", response.Code, response.Body.String())
		}
	})
}

func TestReplicaListingStopsAtTheSnapshotByteCap(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cfg := replicaConfig(t, t.TempDir(), now)
	cfg.Now = func() time.Time { return now }
	cfg.Snapshots = discovery.SnapshotLimits{MaxBytes: 1024}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if added, err := s.Records().Import(keys.DIDOf(devnet.ProviderKey(0)), versionCopies(t), 5); err != nil || added != 5 {
		t.Fatalf("Records().Import() = (%d, %v); want (5, nil)", added, err)
	}
	t.Run("a range whose records pass the cap is refused before it is copied", func(t *testing.T) {
		got := readTestRequest(t, s, http.MethodGet, "/v1/messages?limit=2")
		if got.Code != http.StatusTooManyRequests || got.Body.String() != `{"retry_after_seconds":1,"status":"rate_limited"}` {
			t.Errorf("status %d, body %s; want 429 rate_limited: five records do not fit in 1024 bytes", got.Code, got.Body)
		}
	})
	t.Run("a single record is still served directly", func(t *testing.T) {
		got := readTestRequest(t, s, http.MethodGet, "/v1/messages/fy1%3A4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f")
		if got.Code != http.StatusOK {
			t.Errorf("direct read status %d; want 200: the cap bounds listings only", got.Code)
		}
	})
}

func TestReplicaReadRoutesRequirePublicOriginForManifest(t *testing.T) {
	s := readTestService(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), false)
	t.Run("manifest unavailable without public origin", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodGet, "/.well-known/foryou-operator.json")
		if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("manifest response = %d %q", response.Code, response.Body.String())
		}
	})
	t.Run("policy unavailable without public origin", func(t *testing.T) {
		response := readTestRequest(t, s, http.MethodGet, "/policy")
		if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("policy response = %d %q", response.Code, response.Body.String())
		}
	})
}
