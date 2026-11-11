package foundation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/wire"
)

func TestLogRoutePagesProofsAndObjects(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	first, firstBytes := signedReceipt(t, service, "fy1:"+strings.Repeat("1", 64), "2026-11-11T00:00:00Z")
	if made, err := service.headTick(now.Add(time.Hour)); err != nil || !made {
		t.Fatalf("first head tick = (%t, %v), want (true, nil)", made, err)
	}
	second, _ := signedReceipt(t, service, "fy1:"+strings.Repeat("2", 64), "2026-11-11T00:00:00Z")
	third, _ := signedReceipt(t, service, "fy1:"+strings.Repeat("3", 64), "2026-11-11T00:00:00Z")
	if made, err := service.headTick(now.Add(2 * time.Hour)); err != nil || !made {
		t.Fatalf("third head tick = (%t, %v), want (true, nil)", made, err)
	}

	t.Run("after zero returns entries two and three with latest head", func(t *testing.T) {
		response := logRouteRequest(t, service, "/v1/foundation/log?after=0")
		var page wire.FoundationLogPage
		logRouteCheckSchema(t, response.Body.Bytes(), "FoundationLogPage", &page)
		head, _ := service.LatestHead()
		if response.Code != http.StatusOK || len(page.Entries) != 2 || page.Entries[0].Index != second.Index || page.Entries[1].Index != third.Index || page.Head == nil || page.Head.Root != head.Root {
			t.Fatalf("after page = %#v, want entries 1 and 2 and latest head", page)
		}
	})

	t.Run("omitted after returns all entries", func(t *testing.T) {
		response := logRouteRequest(t, service, "/v1/foundation/log")
		var page wire.FoundationLogPage
		logRouteCheckSchema(t, response.Body.Bytes(), "FoundationLogPage", &page)
		if response.Code != http.StatusOK || len(page.Entries) != 3 || page.Entries[0].Index != first.Index || page.Entries[2].Index != third.Index {
			t.Fatalf("full log page = %#v, want all three entries", page)
		}
	})

	t.Run("proof verifies against signed head", func(t *testing.T) {
		response := logRouteRequest(t, service, "/v1/foundation/log/proof/"+third.ObjectHash+"?size=3")
		var proof wire.FoundationLogProof
		logRouteCheckSchema(t, response.Body.Bytes(), "FoundationLogProof", &proof)
		head, _ := service.LatestHead()
		if response.Code != http.StatusOK || proof.Index != third.Index || proof.Size != 3 || logRouteRoot(t, third.ObjectHash, proof) != head.Root {
			t.Fatalf("proof = %#v, want entry three proof against head root %s", proof, head.Root)
		}
	})

	t.Run("proof excludes an entry outside the requested tree", func(t *testing.T) {
		response := logRouteRequest(t, service, "/v1/foundation/log/proof/"+third.ObjectHash+"?size=2")
		logRouteExpectNotFound(t, response)
	})

	t.Run("proof rejects zero and future sizes", func(t *testing.T) {
		logRouteExpectInvalid(t, logRouteRequest(t, service, "/v1/foundation/log/proof/"+second.ObjectHash+"?size=0"))
		logRouteExpectInvalid(t, logRouteRequest(t, service, "/v1/foundation/log/proof/"+second.ObjectHash+"?size=4"))
	})

	t.Run("objects return exact stored bytes", func(t *testing.T) {
		response := logRouteRequest(t, service, "/v1/foundation/log/objects/"+first.ObjectHash)
		if response.Code != http.StatusOK || string(response.Body.Bytes()) != string(firstBytes) || logRouteObjectHashDigest(response.Body.Bytes()) != first.ObjectHash {
			t.Fatalf("object response = %d hash %s, want exact stored object hash %s", response.Code, logRouteObjectHashDigest(response.Body.Bytes()), first.ObjectHash)
		}
	})

	t.Run("unknown objects return not found", func(t *testing.T) {
		response := logRouteRequest(t, service, "/v1/foundation/log/objects/"+strings.Repeat("0", 64))
		logRouteExpectNotFound(t, response)
	})

	t.Run("heads after size one returns size three", func(t *testing.T) {
		response := logRouteRequest(t, service, "/v1/foundation/log/heads?after_size=1")
		var page wire.FoundationLogHeadsPage
		logRouteCheckSchema(t, response.Body.Bytes(), "FoundationLogHeadsPage", &page)
		if response.Code != http.StatusOK || len(page.Heads) != 1 || page.Heads[0].Size != 3 {
			t.Fatalf("heads page = %#v, want only size-three head", page)
		}
	})

	t.Run("canonical query validation", func(t *testing.T) {
		logRouteExpectInvalid(t, logRouteRequest(t, service, "/v1/foundation/log?after=01"))
		logRouteExpectInvalid(t, logRouteRequest(t, service, "/v1/foundation/log?after=-1"))
		logRouteExpectInvalid(t, logRouteRequest(t, service, "/v1/foundation/log?after=1&after=2"))
		logRouteExpectInvalid(t, logRouteRequest(t, service, "/v1/foundation/log/heads?after_size=01"))
	})
}

func logRouteRequest(t *testing.T, service *Service, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response headers = %#v, want JSON and no-store", response.Header())
	}
	return response
}

func logRouteExpectInvalid(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body apierr.PublicReadError
	if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Status != apierr.InvalidQuery {
		t.Fatalf("response = %d %s, want 400 invalid_query", response.Code, response.Body.String())
	}
}

func logRouteExpectNotFound(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` {
		t.Fatalf("response = %d %s, want 404 not_found", response.Code, response.Body.String())
	}
}

func logRouteCheckSchema(t *testing.T, body []byte, schema string, target any) {
	t.Helper()
	if err := wire.DecodeLimits(body, canon.Limits{MaxDepth: 32}, schema, target); err != nil {
		t.Fatalf("response does not decode as %s: %v", schema, err)
	}
}

func logRouteObjectHashDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func logRouteRoot(t *testing.T, hash string, proof wire.FoundationLogProof) string {
	t.Helper()
	root, err := governance.RootFromProof(hash, proof)
	if err != nil {
		t.Fatal(err)
	}
	return root
}
