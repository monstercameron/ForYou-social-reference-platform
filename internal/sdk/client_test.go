package sdk

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const rdTestServiceID = "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG"

func rdTestURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func rdTestClient(t *testing.T, serverURL string, options Options) *Client {
	t.Helper()
	options.ServiceID = rdTestServiceID
	client, err := New(serverURL, options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNew(t *testing.T) {
	t.Run("preserves service path prefix", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Path; got != "/monitors/2/v1/witnesses" {
				t.Errorf("path=%q; want monitor prefix", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"witnesses":[],"refusals":[],"holds":[],"claims":[]}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL+"/monitors/2/", Options{AllowLoopbackHTTP: true})
		if _, _, err := client.Witnesses(context.Background(), rdTestServiceID, "bitcoin-mainnet", 0); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("accepts explicit loopback HTTP", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()
		if _, err := New(server.URL, Options{ServiceID: rdTestServiceID, AllowLoopbackHTTP: true}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rejects loopback HTTP without the dev flag", func(t *testing.T) {
		if _, err := New("http://127.0.0.1:8445", Options{ServiceID: rdTestServiceID}); err == nil {
			t.Fatal("New accepted loopback HTTP without AllowLoopbackHTTP")
		}
	})
	t.Run("rejects non-loopback HTTP", func(t *testing.T) {
		if _, err := New("http://p.example", Options{ServiceID: rdTestServiceID, AllowLoopbackHTTP: true}); err == nil {
			t.Fatal("New accepted non-loopback HTTP")
		}
	})
	t.Run("rejects userinfo", func(t *testing.T) {
		if _, err := New("https://user:pass@p.example", Options{ServiceID: rdTestServiceID}); err == nil {
			t.Fatal("New accepted URL userinfo")
		}
	})
}

func TestSendIsCredentialFreeAndBounded(t *testing.T) {
	t.Run("does not send cookie or referer", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Cookie") != "" {
				t.Error("request sent Cookie")
			}
			if r.Header.Get("Referer") != "" {
				t.Error("request sent Referer")
			}
			if r.Header.Get("Accept") != "application/json" {
				t.Errorf("Accept=%q", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"successions":[]}`))
		}))
		defer server.Close()
		jar, _ := cookiejar.New(nil)
		provided := &http.Client{Jar: jar}
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true, HTTPClient: provided})
		jar.SetCookies(rdTestURL(t, server.URL), []*http.Cookie{{Name: "session", Value: "secret"}})
		if _, _, _, err := client.Successions(context.Background(), rdTestServiceID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestClassifyAndAmbiguous(t *testing.T) {
	t.Run("marks unavailable as ambiguous", func(t *testing.T) {
		if !IsAmbiguous(&Error{HTTPStatus: 503, Status: "unavailable"}) {
			t.Fatal("503 was not ambiguous")
		}
	})
	t.Run("marks deadline during request as ambiguous", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := client.GetMessage(ctx, "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f")
		if !IsAmbiguous(err) || Classify(err) != SignalTimeout {
			t.Fatalf("IsAmbiguous/Classify=(%t,%s); want (true,timeout)", IsAmbiguous(err), Classify(err))
		}
	})
	t.Run("does not mark rejected request ambiguous", func(t *testing.T) {
		if IsAmbiguous(&Error{HTTPStatus: 422, Status: "bad_authorization"}) {
			t.Fatal("422 was ambiguous")
		}
	})
	t.Run("keeps signal names stable", func(t *testing.T) {
		if got := SignalProtocol.String(); got != "protocol" {
			t.Errorf("String()=%q; want protocol", got)
		}
	})
}

func rdFetchError(t *testing.T, status int, body string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
	_, _, err := client.Settlement(context.Background(), strings.Repeat("A", 43))
	return err
}

func TestPublicErrorMapping(t *testing.T) {
	check := func(t *testing.T, status int, body, wantSignal string) {
		t.Helper()
		err := rdFetchError(t, status, body)
		if got := Classify(err).String(); got != wantSignal {
			t.Fatalf("Classify(error)=%q; want %q (error %v)", got, wantSignal, err)
		}
	}
	t.Run("404 not found", func(t *testing.T) { check(t, 404, `{"status":"not_found"}`, "not_found") })
	t.Run("410 retention expired", func(t *testing.T) { check(t, 410, `{"status":"retention_expired"}`, "retention_expired") })
	t.Run("403 policy restricted", func(t *testing.T) { check(t, 403, `{"status":"policy_restricted"}`, "policy_restricted") })
	t.Run("403 legal restricted", func(t *testing.T) { check(t, 403, `{"status":"legal_restricted"}`, "legal_restricted") })
	t.Run("403 restricted", func(t *testing.T) { check(t, 403, `{"status":"restricted"}`, "restricted") })
	t.Run("429 retry delay", func(t *testing.T) {
		err := rdFetchError(t, 429, `{"retry_after_seconds":30,"status":"rate_limited"}`)
		var responseError *Error
		if !errors.As(err, &responseError) || responseError.RetryAfter != 30*time.Second {
			t.Fatalf("error=%v; want 30 second retry delay", err)
		}
	})
	t.Run("503 requires retry delay and remains ambiguous", func(t *testing.T) {
		err := rdFetchError(t, 503, `{"status":"unavailable"}`)
		if Classify(err) != SignalProtocol || !IsAmbiguous(err) {
			t.Fatalf("Classify/IsAmbiguous=(%s,%t); want (protocol,true)", Classify(err), IsAmbiguous(err))
		}
	})
	t.Run("mismatched status is protocol error", func(t *testing.T) { check(t, 404, `{"status":"restricted"}`, "protocol") })
	t.Run("oversized error body is protocol error", func(t *testing.T) { check(t, 404, strings.Repeat(" ", 1025), "protocol") })
}
