package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const namesAuthorID = "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"

type namesExample struct {
	Record     names.NameRecord `json:"record"`
	Withdrawal names.NameRecord `json:"withdrawal"`
}

// namesLoadExample reads the byte-for-byte copy of examples/name-record.example.json.
func namesLoadExample(t *testing.T) (namesExample, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/name-record.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var example namesExample
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	encoded, err := names.Encode(example.Record)
	if err != nil {
		t.Fatal(err)
	}
	return example, encoded
}

func namesService(t *testing.T, now *time.Time, dataDir string) *Service {
	t.Helper()
	cfg, err := TestConfig(func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dataDir
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func namesAcceptPost(t *testing.T, service *Service, now time.Time) {
	t.Helper()
	const messageID = "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	message := loadExample(t, 0)
	if _, err := service.Records().Accept(Acceptance{MessageID: messageID, Bytes: message, IntentDigest: sha256.Sum256([]byte("names author post")), ReceiptID: "names-receipt", IssuedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func namesRequest(t *testing.T, service *Service, method, target string, body []byte, contentType string) *httptest.ResponseRecorder {
	return namesRequestFrom(t, service, method, target, body, contentType, "192.0.2.20:1234")
}

func namesRequestFrom(t *testing.T, service *Service, method, target string, body []byte, contentType, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.RemoteAddr = remoteAddr
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	return response
}

func namesAssertResponse(t *testing.T, response *httptest.ResponseRecorder, status int, body []byte) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body %s", response.Code, status, response.Body.Bytes())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	if body != nil && !bytes.Equal(response.Body.Bytes(), body) {
		t.Fatalf("body = %s, want %s", response.Body.Bytes(), body)
	}
}

func namesPublish(t *testing.T, service *Service, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return namesRequest(t, service, http.MethodPost, "/v1/names", body, "application/json")
}

func namesRecordBody(t *testing.T, record names.NameRecord) []byte {
	t.Helper()
	body, err := names.Encode(record)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func namesAuthorKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	seed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys.DIDOf(key); got != namesAuthorID {
		t.Fatalf("author DID = %q, want %q", got, namesAuthorID)
	}
	return key
}

func TestNamesPublishOrderAndRead(t *testing.T) {
	t.Run("accepted post, retry, withdrawal, stale record and direct read", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		example, recordBody := namesLoadExample(t)

		namesAssertResponse(t, namesPublish(t, service, recordBody), http.StatusConflict, []byte(`{"status":"not_ready"}`))
		namesAcceptPost(t, service, now)

		namesAssertResponse(t, namesPublish(t, service, recordBody), http.StatusCreated, recordBody)
		namesAssertResponse(t, namesPublish(t, service, recordBody), http.StatusOK, recordBody)

		read := namesRequest(t, service, http.MethodGet, "/v1/names/did%3Akey%3Az6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd", nil, "")
		namesAssertResponse(t, read, http.StatusOK, recordBody)

		now = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
		withdrawalBody := namesRecordBody(t, example.Withdrawal)
		namesAssertResponse(t, namesPublish(t, service, withdrawalBody), http.StatusCreated, withdrawalBody)
		namesAssertResponse(t, namesPublish(t, service, recordBody), http.StatusConflict, []byte(`{"status":"stale_state"}`))
		namesAssertResponse(t, namesRequest(t, service, http.MethodGet, "/v1/names/did%3Akey%3Az6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd", nil, ""), http.StatusOK, withdrawalBody)

		other := "did:key:z6Mkg26jczDiqsPK4momfvhZTTyFefWEyxYiSisFJ2wWJFkg"
		namesAssertResponse(t, namesRequest(t, service, http.MethodGet, "/v1/names/"+strings.ReplaceAll(other, ":", "%3A"), nil, ""), http.StatusNotFound, []byte(`{"status":"not_found"}`))
		namesAssertResponse(t, namesRequest(t, service, http.MethodGet, "/v1/names/did:key:zBad", nil, ""), http.StatusBadRequest, []byte(`{"status":"invalid_query"}`))
	})
}

func TestNamesRateLimit(t *testing.T) {
	t.Run("first change is exempt and next change waits 24 hours", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		example, firstBody := namesLoadExample(t)
		namesAcceptPost(t, service, now)
		namesAssertResponse(t, namesPublish(t, service, firstBody), http.StatusCreated, firstBody)

		name := "Zoë Example"
		key := namesAuthorKey(t)
		next, err := names.Build(key, &name, example.Record.Avatar, now.Add(12*time.Hour), &example.Record.NameID)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(12 * time.Hour)
		limited := namesPublish(t, service, namesRecordBody(t, next))
		namesAssertResponse(t, limited, http.StatusTooManyRequests, []byte(`{"retry_after_seconds":43200,"status":"rate_limited"}`))
		if limited.Header().Get("Retry-After") != "43200" {
			t.Fatalf("Retry-After = %q, want 43200", limited.Header().Get("Retry-After"))
		}

		now = now.Add(12 * time.Hour)
		namesAssertResponse(t, namesPublish(t, service, namesRecordBody(t, next)), http.StatusCreated, namesRecordBody(t, next))
	})
}

func TestNamesInvalidPublish(t *testing.T) {
	checkInvalid := func(t *testing.T, service *Service, body []byte, contentType string) {
		t.Helper()
		namesAssertResponse(t, namesRequest(t, service, http.MethodPost, "/v1/names", body, contentType), http.StatusUnprocessableEntity, []byte(`{"status":"invalid_message"}`))
	}
	t.Run("changed display name has a stale name ID", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		example, _ := namesLoadExample(t)
		badName := "Different"
		mismatch := example.Record
		mismatch.DisplayName = &badName
		checkInvalid(t, service, namesRecordBody(t, mismatch), "application/json")
	})
	t.Run("oversized valid JSON whitespace body", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		_, body := namesLoadExample(t)
		oversized := append([]byte(nil), body...)
		oversized = append(oversized, bytes.Repeat([]byte(" "), names.MaxRecordBytes+1-len(oversized))...)
		checkInvalid(t, service, oversized, "application/json")
	})
	t.Run("wrong content type", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		_, body := namesLoadExample(t)
		checkInvalid(t, service, body, "text/plain")
	})
	t.Run("record beyond clock tolerance", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		futureName := "Future Name"
		future, err := names.Build(namesAuthorKey(t), &futureName, nil, now.Add(61*time.Second), nil)
		if err != nil {
			t.Fatal(err)
		}
		checkInvalid(t, service, namesRecordBody(t, future), "application/json")
	})
	t.Run("duplicate member in otherwise valid object", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		_, body := namesLoadExample(t)
		duplicate := append([]byte(`{"author_id":"`+namesAuthorID+`",`), body[1:]...)
		checkInvalid(t, service, duplicate, "application/json")
	})
}

func TestNamesClockUncertaintyPrecedesVerification(t *testing.T) {
	t.Run("uncertain clock returns unavailable before future-time verification", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		if err := service.CheckClock(); err != nil {
			t.Fatal(err)
		}
		_, body := namesLoadExample(t)
		now = now.Add(-61 * time.Second)
		namesAssertResponse(t, namesPublish(t, service, body), http.StatusServiceUnavailable, []byte(`{"retry_after_seconds":30,"status":"unavailable"}`))
	})
}

func TestNamesReadLimitPrecedesParsing(t *testing.T) {
	t.Run("malformed requests are limited before body validation", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		namesRequest(t, service, http.MethodGet, "/v1/names?unknown=x", nil, "")
		namesRequest(t, service, http.MethodGet, "/v1/names?unknown=x", nil, "")
		namesRequest(t, service, http.MethodGet, "/v1/names?unknown=x", nil, "")
		namesRequest(t, service, http.MethodGet, "/v1/names?unknown=x", nil, "")
		namesRequest(t, service, http.MethodGet, "/v1/names?unknown=x", nil, "")
		namesRequest(t, service, http.MethodGet, "/v1/names?unknown=x", nil, "")
		limited := namesRequest(t, service, http.MethodGet, "/v1/names?unknown=x", nil, "")
		if limited.Code != http.StatusTooManyRequests || !bytes.Contains(limited.Body.Bytes(), []byte(`"status":"rate_limited"`)) {
			t.Fatalf("seventh malformed request = %d %s", limited.Code, limited.Body.Bytes())
		}
	})
}

func TestNamesLookup(t *testing.T) {
	t.Run("NFC and simple folding match and withdrawal disappears", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		example, body := namesLoadExample(t)
		namesAcceptPost(t, service, now)
		namesAssertResponse(t, namesPublish(t, service, body), http.StatusCreated, body)
		page := []byte(fmt.Sprintf(`{"names":[%s],"next_cursor":null}`, body))
		namesAssertResponse(t, namesRequest(t, service, http.MethodGet, "/v1/names?display_name=zo%C3%AB%20example", nil, ""), http.StatusOK, page)
		namesAssertResponse(t, namesRequest(t, service, http.MethodGet, "/v1/names?display_name=ZOE%CC%88%20EXAMPLE", nil, ""), http.StatusOK, page)
		namesAssertResponse(t, namesRequest(t, service, http.MethodGet, "/v1/names?display_name=Zoe", nil, ""), http.StatusOK, []byte(`{"names":[],"next_cursor":null}`))

		now = now.Add(24 * time.Hour)
		withdrawal := namesRecordBody(t, example.Withdrawal)
		namesAssertResponse(t, namesPublish(t, service, withdrawal), http.StatusCreated, withdrawal)
		namesAssertResponse(t, namesRequest(t, service, http.MethodGet, "/v1/names?display_name=zo%C3%AB%20example", nil, ""), http.StatusOK, []byte(`{"names":[],"next_cursor":null}`))
	})
}

func TestNamesLookupValidation(t *testing.T) {
	t.Run("query names, multiplicity and cursor encoding", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		namesAssertResponse(t, namesRequestFrom(t, service, http.MethodGet, "/v1/names", nil, "", "192.0.2.1:1"), http.StatusBadRequest, []byte(`{"status":"invalid_query"}`))
		namesAssertResponse(t, namesRequestFrom(t, service, http.MethodGet, "/v1/names?display_name=x&display_name=x", nil, "", "192.0.2.2:1"), http.StatusBadRequest, []byte(`{"status":"invalid_query"}`))
		namesAssertResponse(t, namesRequestFrom(t, service, http.MethodGet, "/v1/names?limit=5&display_name=x", nil, "", "192.0.2.3:1"), http.StatusBadRequest, []byte(`{"status":"invalid_query"}`))
		namesAssertResponse(t, namesRequestFrom(t, service, http.MethodGet, "/v1/names?display_name=", nil, "", "192.0.2.4:1"), http.StatusBadRequest, []byte(`{"status":"invalid_query"}`))
		namesAssertResponse(t, namesRequestFrom(t, service, http.MethodGet, "/v1/names?display_name=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil, "", "192.0.2.5:1"), http.StatusBadRequest, []byte(`{"status":"invalid_query"}`))
		namesAssertResponse(t, namesRequestFrom(t, service, http.MethodGet, "/v1/names?display_name=x&cursor=abc", nil, "", "192.0.2.6:1"), http.StatusBadRequest, []byte(`{"status":"invalid_cursor"}`))
		namesAssertResponse(t, namesRequestFrom(t, service, http.MethodGet, "/v1/names?display_name=x+y", nil, "", "192.0.2.7:1"), http.StatusOK, []byte(`{"names":[],"next_cursor":null}`))
		namesAssertResponse(t, namesRequestFrom(t, service, http.MethodGet, "/v1/names?display_name=%252F", nil, "", "192.0.2.8:1"), http.StatusBadRequest, []byte(`{"status":"invalid_query"}`))
	})
}

func namesSeedAuthor(t *testing.T, service *Service, number int, now time.Time) string {
	t.Helper()
	seed := make([]byte, 32)
	seed[0] = byte(number)
	seed[1] = byte(number >> 8)
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	displayName := "Ada"
	record, err := names.Build(key, &displayName, nil, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded := namesRecordBody(t, record)
	fold := names.FoldKey(displayName) + "\x1f" + record.AuthorID
	if err := service.Store().Update(func(tx store.Tx) error {
		if err := tx.Put(namesCurrentCollection, record.AuthorID, encoded); err != nil {
			return err
		}
		if err := tx.Put(namesChangedCollection, record.AuthorID, []byte(wire.FormatTime(now))); err != nil {
			return err
		}
		return tx.Put(namesFoldCollection, fold, []byte("1"))
	}); err != nil {
		t.Fatal(err)
	}
	return record.AuthorID
}

func TestNamesLookupPaging(t *testing.T) {
	t.Run("snapshot cursors repeat and bind query and expiry", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		namesSeedAuthor(t, service, 0, now)
		namesSeedAuthor(t, service, 1, now)
		namesSeedAuthor(t, service, 2, now)
		namesSeedAuthor(t, service, 3, now)
		namesSeedAuthor(t, service, 4, now)
		namesSeedAuthor(t, service, 5, now)
		namesSeedAuthor(t, service, 6, now)
		namesSeedAuthor(t, service, 7, now)
		namesSeedAuthor(t, service, 8, now)
		namesSeedAuthor(t, service, 9, now)
		namesSeedAuthor(t, service, 10, now)
		namesSeedAuthor(t, service, 11, now)
		namesSeedAuthor(t, service, 12, now)
		namesSeedAuthor(t, service, 13, now)
		namesSeedAuthor(t, service, 14, now)
		namesSeedAuthor(t, service, 15, now)
		namesSeedAuthor(t, service, 16, now)
		namesSeedAuthor(t, service, 17, now)
		namesSeedAuthor(t, service, 18, now)
		namesSeedAuthor(t, service, 19, now)
		namesSeedAuthor(t, service, 20, now)
		namesSeedAuthor(t, service, 21, now)
		namesSeedAuthor(t, service, 22, now)
		namesSeedAuthor(t, service, 23, now)
		namesSeedAuthor(t, service, 24, now)
		namesSeedAuthor(t, service, 25, now)
		namesSeedAuthor(t, service, 26, now)
		namesSeedAuthor(t, service, 27, now)
		namesSeedAuthor(t, service, 28, now)
		namesSeedAuthor(t, service, 29, now)
		namesSeedAuthor(t, service, 30, now)
		namesSeedAuthor(t, service, 31, now)
		namesSeedAuthor(t, service, 32, now)
		namesSeedAuthor(t, service, 33, now)
		namesSeedAuthor(t, service, 34, now)
		namesSeedAuthor(t, service, 35, now)
		namesSeedAuthor(t, service, 36, now)
		namesSeedAuthor(t, service, 37, now)
		namesSeedAuthor(t, service, 38, now)
		namesSeedAuthor(t, service, 39, now)
		namesSeedAuthor(t, service, 40, now)
		namesSeedAuthor(t, service, 41, now)
		namesSeedAuthor(t, service, 42, now)
		namesSeedAuthor(t, service, 43, now)
		namesSeedAuthor(t, service, 44, now)
		namesSeedAuthor(t, service, 45, now)
		namesSeedAuthor(t, service, 46, now)
		namesSeedAuthor(t, service, 47, now)
		namesSeedAuthor(t, service, 48, now)
		namesSeedAuthor(t, service, 49, now)
		namesSeedAuthor(t, service, 50, now)

		first := namesRequest(t, service, http.MethodGet, "/v1/names?display_name=Ada", nil, "")
		if first.Code != http.StatusOK {
			t.Fatalf("first page status = %d: %s", first.Code, first.Body.Bytes())
		}
		parsed, err := names.ParsePage(first.Body.Bytes())
		if err != nil || len(parsed.Names) != 50 || parsed.NextCursor == nil {
			t.Fatalf("first page = %d records, cursor %v, error %v", len(parsed.Names), parsed.NextCursor, err)
		}
		if !slices.IsSortedFunc(parsed.Names, func(a, b names.NameRecord) int { return strings.Compare(a.AuthorID, b.AuthorID) }) {
			t.Fatal("first page authors are not sorted by author_id")
		}
		repeated := namesRequest(t, service, http.MethodGet, "/v1/names?display_name=Ada&cursor="+*parsed.NextCursor, nil, "")
		repeatedAgain := namesRequest(t, service, http.MethodGet, "/v1/names?display_name=Ada&cursor="+*parsed.NextCursor, nil, "")
		if !bytes.Equal(repeated.Body.Bytes(), repeatedAgain.Body.Bytes()) {
			t.Fatalf("repeating continuation cursor changed bytes")
		}
		second := namesRequest(t, service, http.MethodGet, "/v1/names?display_name=Ada&cursor="+*parsed.NextCursor, nil, "")
		secondPage, err := names.ParsePage(second.Body.Bytes())
		if err != nil || len(secondPage.Names) != 1 || secondPage.NextCursor != nil {
			t.Fatalf("second page = %d records, cursor %v, error %v", len(secondPage.Names), secondPage.NextCursor, err)
		}
		if secondPage.Names[0].AuthorID <= parsed.Names[len(parsed.Names)-1].AuthorID {
			t.Fatalf("page 2 author %q is not after page 1 author %q", secondPage.Names[0].AuthorID, parsed.Names[len(parsed.Names)-1].AuthorID)
		}
		mismatch := namesRequest(t, service, http.MethodGet, "/v1/names?display_name=Bob&cursor="+*parsed.NextCursor, nil, "")
		if mismatch.Code != http.StatusBadRequest || !bytes.Contains(mismatch.Body.Bytes(), []byte(`"status":"invalid_cursor"`)) {
			t.Fatalf("mismatched cursor = %d %s", mismatch.Code, mismatch.Body.Bytes())
		}
		if err := service.Store().Update(func(tx store.Tx) error {
			return tx.Delete(namesCurrentCollection, parsed.Names[0].AuthorID)
		}); err != nil {
			t.Fatal(err)
		}
		fresh := namesRequestFrom(t, service, http.MethodGet, "/v1/names?display_name=Ada", nil, "", "192.0.2.31:1")
		freshPage, err := names.ParsePage(fresh.Body.Bytes())
		if err != nil || len(freshPage.Names) != 49 || freshPage.NextCursor == nil {
			t.Fatalf("fresh page after current record removal = %d records, cursor %v, error %v", len(freshPage.Names), freshPage.NextCursor, err)
		}
		freshSecond := namesRequestFrom(t, service, http.MethodGet, "/v1/names?display_name=Ada&cursor="+*freshPage.NextCursor, nil, "", "192.0.2.32:1")
		freshSecondPage, err := names.ParsePage(freshSecond.Body.Bytes())
		if err != nil || len(freshSecondPage.Names) != 1 || freshSecondPage.NextCursor != nil || freshSecondPage.Names[0].AuthorID != secondPage.Names[0].AuthorID {
			t.Fatalf("fresh page 2 = %#v, cursor %v, error %v; want snapshot's final author %q", freshSecondPage.Names, freshSecondPage.NextCursor, err, secondPage.Names[0].AuthorID)
		}
		now = now.Add(15 * time.Minute)
		expired := namesRequest(t, service, http.MethodGet, "/v1/names?display_name=Ada&cursor="+*parsed.NextCursor, nil, "")
		if expired.Code != http.StatusBadRequest || !bytes.Contains(expired.Body.Bytes(), []byte(`"status":"invalid_cursor"`)) {
			t.Fatalf("expired cursor = %d %s", expired.Code, expired.Body.Bytes())
		}
	})
}

func TestNamesRestart(t *testing.T) {
	t.Run("current record and last change survive reopening the store", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		dir := filepath.Join(t.TempDir(), "provider")
		service := namesService(t, &now, dir)
		example, body := namesLoadExample(t)
		namesAcceptPost(t, service, now)
		namesAssertResponse(t, namesPublish(t, service, body), http.StatusCreated, body)
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}

		cfg, err := TestConfig(func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		cfg.DataDir = dir
		restarted, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = restarted.Close() })
		read := namesRequest(t, restarted, http.MethodGet, "/v1/names/did%3Akey%3Az6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd", nil, "")
		namesAssertResponse(t, read, http.StatusOK, body)

		now = now.Add(12 * time.Hour)
		newName := "A different name"
		next, err := names.Build(namesAuthorKey(t), &newName, nil, now, &example.Record.NameID)
		if err != nil {
			t.Fatal(err)
		}
		limited := namesPublish(t, restarted, namesRecordBody(t, next))
		if limited.Code != http.StatusTooManyRequests || !bytes.Contains(limited.Body.Bytes(), []byte(`"retry_after_seconds":43200`)) {
			t.Fatalf("post-restart rate limit = %d %s", limited.Code, limited.Body.Bytes())
		}
	})
}

func TestNamesAvatarDoesNotFetch(t *testing.T) {
	t.Run("signed avatar reference is returned unchanged", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := namesService(t, &now, "")
		_, body := namesLoadExample(t)
		namesAcceptPost(t, service, now)
		response := namesPublish(t, service, body)
		namesAssertResponse(t, response, http.StatusCreated, body)
		if !bytes.Contains(response.Body.Bytes(), []byte(`"avatar":{"sha256":"08b860e2b03a68a952acf3d1c63b988f251a509aca0420ce984de33f31c8bf28","size_bytes":35,"url":"https://media.example/avatars/zoe.png"}`)) {
			t.Fatal("avatar reference did not remain in the returned signed record")
		}
	})
}
