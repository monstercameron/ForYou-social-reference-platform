package sdk

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func wtWitness() wire.SettlementWitness {
	return wire.SettlementWitness{
		WitnessID: rdTestServiceID, ClaimID: "fysc2:a3854d69cc661226d0ec03420f6d0e892a2be098f7609e027231592ba199d8ae", ProviderID: rdTestServiceID,
		Network: wire.BitcoinMainnet, Sequence: 1, PeriodEnd: "2026-09-30T23:59:59Z",
		TotalBaseUnits: "1", CumulativeBaseUnits: "1", ReceiptsRoot: "a3b611487ed295e46340f4132f3281d8c32e0e8dc9546776be535594c2e3cfa9", CreditBaseUnits: "0",
		WitnessedAt: "2026-10-01T00:00:00Z", WitnessSignature: "a46XqRyKAr3IuXZP_Yi3AIf5Q3fxilGqC22i4E_NPGLJEvSIpl2xvBjQDOgI-wdHVS84GOH_gK246Bzg8W_RCw",
	}
}

func wtClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, Options{ServiceID: rdTestServiceID, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestDeliverWitness(t *testing.T) {
	witness := wtWitness()
	canonical, err := wire.Encode("SettlementWitness", witness)
	if err != nil {
		t.Fatal(err)
	}
	checkStatus := func(t *testing.T, responseStatus, want int) {
		t.Helper()
		client := wtClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/settlement-witnesses" {
				t.Errorf("request = %s %s", r.Method, r.URL.Path)
			}
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil || string(body) != string(canonical) {
				t.Errorf("body differs from canonical witness: %v", readErr)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(responseStatus)
			_, _ = io.WriteString(w, `{}`)
		}))
		got, err := client.DeliverWitness(context.Background(), witness)
		if err != nil || got != want {
			t.Fatalf("DeliverWitness() = %d, %v; want %d, nil", got, err, want)
		}
	}
	t.Run("201 newly held", func(t *testing.T) { checkStatus(t, http.StatusCreated, http.StatusCreated) })
	t.Run("200 already held", func(t *testing.T) { checkStatus(t, http.StatusOK, http.StatusOK) })
	t.Run("404 not found", func(t *testing.T) {
		client := wtClient(t, wtErrorServer(http.StatusNotFound, `{"status":"not_found"}`))
		status, err := client.DeliverWitness(context.Background(), witness)
		var publicError *Error
		if status != http.StatusNotFound || !errors.As(err, &publicError) || publicError.Status != "not_found" || Classify(err) != SignalNotFound {
			t.Fatalf("= %d, %v", status, err)
		}
	})
	t.Run("422 invalid message", func(t *testing.T) {
		client := wtClient(t, wtErrorServer(http.StatusUnprocessableEntity, `{"status":"invalid_message"}`))
		status, err := client.DeliverWitness(context.Background(), witness)
		var publicError *Error
		if status != http.StatusUnprocessableEntity || !errors.As(err, &publicError) || publicError.Status != "invalid_message" {
			t.Fatalf("= %d, %v", status, err)
		}
	})
	t.Run("malformed server error is protocol and ambiguous", func(t *testing.T) {
		client := wtClient(t, wtErrorServer(http.StatusInternalServerError, `{not-json`))
		_, err := client.DeliverWitness(context.Background(), witness)
		if err == nil || Classify(err) != SignalProtocol || !IsAmbiguous(err) {
			t.Fatalf("error = %v; want protocol and ambiguous", err)
		}
	})
}

func wtErrorServer(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}
