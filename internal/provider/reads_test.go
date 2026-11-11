package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
)

type rdControls struct {
	mu           sync.RWMutex
	restrictions map[string]Restriction
	err          error
}

type rdPage struct {
	GeneratedAt string            `json:"generated_at"`
	ExpiresAt   string            `json:"expires_at"`
	Messages    []json.RawMessage `json:"messages"`
	NextCursor  *string           `json:"next_cursor"`
}

type rdChangesPage struct {
	Items []struct {
		Sequence uint64 `json:"sequence"`
		Message  struct {
			MessageID string `json:"message_id"`
		} `json:"message"`
	} `json:"items"`
	More       bool   `json:"more"`
	NextAfter  uint64 `json:"next_after"`
	ProviderID string `json:"provider_id"`
}

func (c *rdControls) Restriction(id string) (Restriction, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.restrictions[id], c.err
}

func (c *rdControls) Ready() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (c *rdControls) rdSet(id string, restriction Restriction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restrictions[id] = restriction
}

func (c *rdControls) rdFail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

func rdReadService(t *testing.T, now *time.Time) (*Service, *rdControls) {
	t.Helper()
	cfg, err := TestConfig(func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Key, err = keys.NewKeyFromSeed([]byte{
		32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47,
		48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63,
	})
	if err != nil {
		t.Fatal(err)
	}
	controls := &rdControls{restrictions: make(map[string]Restriction)}
	old := routeGroups
	routeGroups = append(routeGroups, func(s *Service, _ *http.ServeMux) { s.SetControls(controls) })
	t.Cleanup(func() { routeGroups = old })
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rdAcceptMessages(t, s, rdExampleMessages(t), 0, *now)
	return s, controls
}

func rdExampleMessages(t *testing.T) []json.RawMessage {
	t.Helper()
	version, err := os.ReadFile("testdata/post-version-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	thread, err := os.ReadFile("testdata/post-comment-thread.json")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := os.ReadFile("testdata/post-with-refs.json")
	if err != nil {
		t.Fatal(err)
	}
	var versionDoc struct {
		Messages []json.RawMessage `json:"messages"`
	}
	var threadDoc struct {
		Messages []json.RawMessage `json:"messages"`
	}
	var refsDoc struct {
		Message json.RawMessage `json:"message"`
	}
	if err = json.Unmarshal(version, &versionDoc); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(thread, &threadDoc); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(refs, &refsDoc); err != nil {
		t.Fatal(err)
	}
	return append(append(versionDoc.Messages, threadDoc.Messages...), refsDoc.Message)
}

func rdAcceptMessages(t *testing.T, s *Service, messages []json.RawMessage, index int, issued time.Time) {
	t.Helper()
	if index == len(messages) {
		return
	}
	bytes, err := canon.Canonicalize(messages[index], canon.MessageLimits)
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err = json.Unmarshal(bytes, &message); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Records().Accept(Acceptance{MessageID: message.MessageID, Bytes: bytes, IntentDigest: sha256.Sum256(bytes), IssuedAt: issued}); err != nil {
		t.Fatal(err)
	}
	rdAcceptMessages(t, s, messages, index+1, issued)
}

func rdRequest(t *testing.T, s *Service, method, target, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func rdSendDirect(t *testing.T, s *Service, target, remote string, remaining int) {
	t.Helper()
	if remaining == 0 {
		return
	}
	response := rdRequest(t, s, http.MethodGet, target, remote)
	if response.Code != http.StatusOK {
		t.Fatalf("direct request %d from %s = %d %q", 31-remaining, remote, response.Code, response.Body.String())
	}
	rdSendDirect(t, s, target, remote, remaining-1)
}

func rdDecodePage(t *testing.T, response *httptest.ResponseRecorder) rdPage {
	t.Helper()
	var page rdPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	return page
}

func rdPageID(t *testing.T, page rdPage, index int) string {
	t.Helper()
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(page.Messages[index], &message); err != nil {
		t.Fatal(err)
	}
	return message.MessageID
}

func TestProviderDirectRead(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, _ := rdReadService(t, &now)
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	want, err := canon.Canonicalize(loadExample(t, 0), canon.MessageLimits)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("exact body and cache policy", func(t *testing.T) {
		response := rdRequest(t, s, http.MethodGet, "/v1/messages/"+id, "192.0.2.10:1234")
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), want) || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "public, max-age=3600" {
			t.Fatalf("GET = %d, headers=%v, body matches=%t", response.Code, response.Header(), bytes.Equal(response.Body.Bytes(), want))
		}
	})
	t.Run("encoded ID ignores Range and conditional headers", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/messages/fy1%3A4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f", nil)
		r.RemoteAddr = "192.0.2.11:1234"
		r.Header.Set("Range", "bytes=0-10")
		r.Header.Set("If-None-Match", "*")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, r)
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), want) || response.Header().Get("ETag") != "" || response.Header().Get("Last-Modified") != "" || response.Header().Get("Accept-Ranges") != "" {
			t.Fatalf("conditional Range response = %d, %v, full=%t", response.Code, response.Header(), bytes.Equal(response.Body.Bytes(), want))
		}
	})
	t.Run("HEAD has matching length without a body", func(t *testing.T) {
		response := rdRequest(t, s, http.MethodHead, "/v1/messages/"+id, "192.0.2.12:1234")
		if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") != strconv.Itoa(len(want)) || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("HEAD = %d headers=%v body=%d", response.Code, response.Header(), response.Body.Len())
		}
	})
	t.Run("unknown and concealed have identical responses", func(t *testing.T) {
		localNow := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, controls := rdReadService(t, &localNow)
		unknown := rdRequest(t, service, http.MethodGet, "/v1/messages/fy1:"+strings.Repeat("0", 64), "192.0.2.13:1234")
		controls.rdSet(id, Concealed)
		concealed := rdRequest(t, service, http.MethodGet, "/v1/messages/"+id, "192.0.2.14:1234")
		if unknown.Code != http.StatusNotFound || concealed.Code != unknown.Code || unknown.Body.String() != `{"status":"not_found"}` || !bytes.Equal(unknown.Body.Bytes(), concealed.Body.Bytes()) || !reflect.DeepEqual(unknown.Header(), concealed.Header()) {
			t.Fatalf("unknown/concealed = %d %q / %d %q", unknown.Code, unknown.Body.String(), concealed.Code, concealed.Body.String())
		}
	})
	t.Run("malformed lowercase escape is not found", func(t *testing.T) {
		response := rdRequest(t, s, http.MethodGet, "/v1/messages/fy1%3a"+strings.Repeat("a", 64), "192.0.2.15:1234")
		if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` {
			t.Fatalf("malformed ID = %d %q", response.Code, response.Body.String())
		}
	})
	t.Run("restricted status beats expiry", func(t *testing.T) {
		localNow := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, controls := rdReadService(t, &localNow)
		controls.rdSet(id, PolicyRestricted)
		deadline := localNow.Add(time.Second)
		if err := service.Records().SetRetainUntil(id, deadline); err != nil {
			t.Fatal(err)
		}
		localNow = deadline.Add(time.Second)
		if err := service.Records().Expire(id, localNow); err != nil {
			t.Fatal(err)
		}
		response := rdRequest(t, service, http.MethodGet, "/v1/messages/"+id, "192.0.2.16:1234")
		if response.Code != http.StatusForbidden || response.Body.String() != `{"status":"policy_restricted"}` {
			t.Fatalf("restricted expired response = %d %q", response.Code, response.Body.String())
		}
	})
	t.Run("legal and generic restrictions expose only their status", func(t *testing.T) {
		check := func(t *testing.T, messageID string, restriction Restriction, status string) {
			t.Helper()
			localNow := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
			service, controls := rdReadService(t, &localNow)
			controls.rdSet(messageID, restriction)
			response := rdRequest(t, service, http.MethodGet, "/v1/messages/"+messageID, "192.0.2.18:1234")
			if response.Code != http.StatusForbidden || response.Body.String() != `{"status":"`+status+`"}` {
				t.Fatalf("%s response = %d %q", status, response.Code, response.Body.String())
			}
		}
		t.Run("legal restricted", func(t *testing.T) {
			check(t, "fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72", LegalRestricted, "legal_restricted")
		})
		t.Run("policy restricted", func(t *testing.T) {
			check(t, id, PolicyRestricted, "policy_restricted")
		})
		t.Run("restricted", func(t *testing.T) {
			check(t, "fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b", Restricted, "restricted")
		})
	})
	t.Run("expired record returns 410 and concealment masks expiry", func(t *testing.T) {
		localNow := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, controls := rdReadService(t, &localNow)
		messageID := "fy1:e703e832f979cb8c1dc232ad719a4f09117617c7852774121e90b4ca2e345262"
		deadline := localNow.Add(time.Second)
		if err := service.Records().SetRetainUntil(messageID, deadline); err != nil {
			t.Fatal(err)
		}
		localNow = deadline.Add(time.Second)
		if err := service.Records().Expire(messageID, localNow); err != nil {
			t.Fatal(err)
		}
		response := rdRequest(t, service, http.MethodGet, "/v1/messages/"+messageID, "192.0.2.19:1234")
		if response.Code != http.StatusGone || response.Body.String() != `{"status":"retention_expired"}` {
			t.Fatalf("expired response = %d %q", response.Code, response.Body.String())
		}
		controls.rdSet(messageID, Concealed)
		concealed := rdRequest(t, service, http.MethodGet, "/v1/messages/"+messageID, "192.0.2.29:1234")
		if concealed.Code != http.StatusNotFound || concealed.Body.String() != `{"status":"not_found"}` {
			t.Fatalf("concealed expired response = %d %q", concealed.Code, concealed.Body.String())
		}
	})
	t.Run("control error fails closed", func(t *testing.T) {
		localNow := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, controls := rdReadService(t, &localNow)
		controls.rdFail(errors.New("control unavailable"))
		response := rdRequest(t, service, http.MethodGet, "/v1/messages/"+id, "192.0.2.17:1234")
		if response.Code != http.StatusServiceUnavailable || response.Body.String() != `{"retry_after_seconds":5,"status":"unavailable"}` {
			t.Fatalf("control error response = %d %q", response.Code, response.Body.String())
		}
	})
}

func TestProviderListingsAndChanges(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, _ := rdReadService(t, &now)
	t.Run("listing and index routes use discovery query validation", func(t *testing.T) {
		missingAuthor := rdRequest(t, s, http.MethodGet, "/v1/author-messages", "192.0.2.20:1234")
		badCursor := rdRequest(t, s, http.MethodGet, "/v1/messages?cursor=short", "192.0.2.21:1234")
		badPath := rdRequest(t, s, http.MethodGet, "/v1/replies/fy1:52ef5b8da8d2dfe1da62510f148807fe1e098ba633299e2722a7c5f529981f67", "192.0.2.22:1234")
		if missingAuthor.Code != http.StatusBadRequest || missingAuthor.Body.String() != `{"status":"invalid_query"}` || badCursor.Code != http.StatusBadRequest || badCursor.Body.String() != `{"status":"invalid_cursor"}` || badPath.Code != http.StatusBadRequest || badPath.Body.String() != `{"status":"invalid_query"}` {
			t.Fatalf("validation statuses = %d/%q, %d/%q, %d/%q", missingAuthor.Code, missingAuthor.Body.String(), badCursor.Code, badCursor.Body.String(), badPath.Code, badPath.Body.String())
		}
	})
	t.Run("snapshots paginate with stable timestamps and current visibility", func(t *testing.T) {
		localNow := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, controls := rdReadService(t, &localNow)
		firstResponse := rdRequest(t, service, http.MethodGet, "/v1/messages?limit=2", "192.0.2.40:1234")
		first := rdDecodePage(t, firstResponse)
		if firstResponse.Code != http.StatusOK || len(first.Messages) != 2 || first.NextCursor == nil || rdPageID(t, first, 0) != "fy1:e703e832f979cb8c1dc232ad719a4f09117617c7852774121e90b4ca2e345262" || rdPageID(t, first, 1) != "fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf" {
			t.Fatalf("first listing page = %+v", first)
		}
		generated, err := time.Parse(time.RFC3339, first.GeneratedAt)
		if err != nil {
			t.Fatal(err)
		}
		expires, err := time.Parse(time.RFC3339, first.ExpiresAt)
		if err != nil || expires.Sub(generated) != 15*time.Minute {
			t.Fatalf("snapshot expiry = %s - %s, error %v", first.ExpiresAt, first.GeneratedAt, err)
		}
		restrictedID := "fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b"
		controls.rdSet(restrictedID, Concealed)
		service.MarkHidden(restrictedID)
		secondResponse := rdRequest(t, service, http.MethodGet, "/v1/messages?limit=2&cursor="+*first.NextCursor, "192.0.2.40:1234")
		second := rdDecodePage(t, secondResponse)
		if secondResponse.Code != http.StatusOK || second.GeneratedAt != first.GeneratedAt || second.ExpiresAt != first.ExpiresAt || len(second.Messages) != 2 || rdPageID(t, second, 0) != "fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72" || rdPageID(t, second, 1) != "fy1:fd23c3b0539258425ec1c0faa056465777e5d5b2d92ef390a71f164748b1f6f8" || second.NextCursor == nil {
			t.Fatalf("second listing page = %+v", second)
		}
		thirdResponse := rdRequest(t, service, http.MethodGet, "/v1/messages?limit=2&cursor="+*second.NextCursor, "192.0.2.40:1234")
		third := rdDecodePage(t, thirdResponse)
		if thirdResponse.Code != http.StatusOK || third.GeneratedAt != first.GeneratedAt || third.ExpiresAt != first.ExpiresAt || len(third.Messages) != 2 || rdPageID(t, third, 0) != "fy1:42b8efb077bb3542a6a49cdab628e002571d5efcd841fb187142faa80c4a3fad" || rdPageID(t, third, 1) != "fy1:52ef5b8da8d2dfe1da62510f148807fe1e098ba633299e2722a7c5f529981f67" || third.NextCursor == nil {
			t.Fatalf("third listing page = %+v", third)
		}
		fourthResponse := rdRequest(t, service, http.MethodGet, "/v1/messages?limit=2&cursor="+*third.NextCursor, "192.0.2.40:1234")
		fourth := rdDecodePage(t, fourthResponse)
		if fourthResponse.Code != http.StatusOK || fourth.GeneratedAt != first.GeneratedAt || fourth.ExpiresAt != first.ExpiresAt || fourth.NextCursor != nil || len(fourth.Messages) != 2 || rdPageID(t, fourth, 0) != "fy1:2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18" || rdPageID(t, fourth, 1) != "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f" {
			t.Fatalf("fourth listing page = %+v", fourth)
		}
	})
	t.Run("cursor from another route is invalid", func(t *testing.T) {
		first := rdDecodePage(t, rdRequest(t, s, http.MethodGet, "/v1/messages?limit=1", "192.0.2.41:1234"))
		if first.NextCursor == nil {
			t.Fatal("listing has no cursor")
		}
		response := rdRequest(t, s, http.MethodGet, "/v1/replies/fy1%3A52ef5b8da8d2dfe1da62510f148807fe1e098ba633299e2722a7c5f529981f67?limit=1&cursor="+*first.NextCursor, "192.0.2.42:1234")
		if response.Code != http.StatusBadRequest || response.Body.String() != `{"status":"invalid_cursor"}` {
			t.Fatalf("cross-route cursor = %d %q", response.Code, response.Body.String())
		}
	})
	t.Run("reply version and reference indexes match signed predicates", func(t *testing.T) {
		replies := rdRequest(t, s, http.MethodGet, "/v1/replies/fy1%3A52ef5b8da8d2dfe1da62510f148807fe1e098ba633299e2722a7c5f529981f67?limit=100", "192.0.2.23:1234")
		versions := rdRequest(t, s, http.MethodGet, "/v1/versions/fy1%3A4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f?limit=100", "192.0.2.24:1234")
		references := rdRequest(t, s, http.MethodGet, "/v1/references/did%3Akey%3Az6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG", "192.0.2.25:1234")
		replyPage := rdDecodePage(t, replies)
		versionPage := rdDecodePage(t, versions)
		referencePage := rdDecodePage(t, references)
		if replies.Code != http.StatusOK || len(replyPage.Messages) != 1 || rdPageID(t, replyPage, 0) != "fy1:42b8efb077bb3542a6a49cdab628e002571d5efcd841fb187142faa80c4a3fad" || replyPage.NextCursor != nil || versions.Code != http.StatusOK || len(versionPage.Messages) != 2 || rdPageID(t, versionPage, 0) != "fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf" || rdPageID(t, versionPage, 1) != "fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72" || versionPage.NextCursor != nil || references.Code != http.StatusOK || len(referencePage.Messages) != 1 || rdPageID(t, referencePage, 0) != "fy1:2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18" || referencePage.NextCursor != nil {
			t.Fatalf("index bodies: replies=%d %s versions=%d %s references=%d %s", replies.Code, replies.Body.String(), versions.Code, versions.Body.String(), references.Code, references.Body.String())
		}
	})
	t.Run("reply lookup does not resolve or disclose its parent", func(t *testing.T) {
		localNow := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, controls := rdReadService(t, &localNow)
		parent := "fy1:52ef5b8da8d2dfe1da62510f148807fe1e098ba633299e2722a7c5f529981f67"
		controls.rdSet(parent, Concealed)
		concealedParent := rdRequest(t, service, http.MethodGet, "/v1/replies/fy1%3A52ef5b8da8d2dfe1da62510f148807fe1e098ba633299e2722a7c5f529981f67", "192.0.2.43:1234")
		page := rdDecodePage(t, concealedParent)
		if concealedParent.Code != http.StatusOK || len(page.Messages) != 1 || rdPageID(t, page, 0) != "fy1:42b8efb077bb3542a6a49cdab628e002571d5efcd841fb187142faa80c4a3fad" || page.NextCursor != nil {
			t.Fatalf("concealed parent replies = %d %s", concealedParent.Code, concealedParent.Body.String())
		}
		unknownParent := rdRequest(t, service, http.MethodGet, "/v1/replies/fy1%3A"+strings.Repeat("0", 64), "192.0.2.44:1234")
		unknownPage := rdDecodePage(t, unknownParent)
		if unknownParent.Code != http.StatusOK || len(unknownPage.Messages) != 0 || unknownPage.NextCursor != nil || !strings.Contains(unknownParent.Body.String(), `"messages":[]`) {
			t.Fatalf("unknown parent replies = %d %s", unknownParent.Code, unknownParent.Body.String())
		}
	})
	t.Run("changes report acceptance sequence", func(t *testing.T) {
		response := rdRequest(t, s, http.MethodGet, "/v1/changes?after=0&limit=100", "192.0.2.26:1234")
		var page rdChangesPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(page.Items) != 9 || page.Items[0].Sequence != 1 || page.Items[1].Sequence != 2 || page.Items[2].Sequence != 3 || page.Items[3].Sequence != 4 || page.Items[4].Sequence != 5 || page.Items[5].Sequence != 6 || page.Items[6].Sequence != 7 || page.Items[7].Sequence != 8 || page.Items[8].Sequence != 9 || page.NextAfter != 9 || page.More || page.ProviderID != "did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2" {
			t.Fatalf("changes response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("changes advance through hidden records and empty tails", func(t *testing.T) {
		localNow := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, controls := rdReadService(t, &localNow)
		hiddenID := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
		controls.rdSet("fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b", Unrestricted)
		controls.rdSet(hiddenID, Concealed)
		response := rdRequest(t, service, http.MethodGet, "/v1/changes?after=0&limit=100", "192.0.2.27:1234")
		var page rdChangesPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(page.Items) != 8 || page.NextAfter != 9 || page.Items[0].Sequence != 2 || page.Items[0].Message.MessageID == hiddenID {
			t.Fatalf("hidden changes response = %d %s", response.Code, response.Body.String())
		}
		tail := rdRequest(t, service, http.MethodGet, "/v1/changes?after=99&limit=100", "192.0.2.28:1234")
		if tail.Code != http.StatusOK || tail.Body.String() != `{"items":[],"more":false,"next_after":99,"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2"}` {
			t.Fatalf("changes tail = %d %s", tail.Code, tail.Body.String())
		}
	})
	t.Run("rate limit is per source", func(t *testing.T) {
		target := "/v1/messages/fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72"
		rdSendDirect(t, s, target, "192.0.2.30:1234", 30)
		limited := rdRequest(t, s, http.MethodGet, target, "192.0.2.30:1234")
		retry, err := strconv.Atoi(limited.Header().Get("Retry-After"))
		if limited.Code != http.StatusTooManyRequests || err != nil || retry < 1 || !strings.Contains(limited.Body.String(), `"status":"rate_limited"`) {
			t.Fatalf("31st request = %d headers=%v body=%q", limited.Code, limited.Header(), limited.Body.String())
		}
		otherSource := rdRequest(t, s, http.MethodGet, target, "192.0.2.31:1234")
		if otherSource.Code != http.StatusOK {
			t.Fatalf("other source request = %d", otherSource.Code)
		}
	})
}
