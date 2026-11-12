package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForReceipts(t *testing.T) {
	t.Run("404 twice then settled proof", func(t *testing.T) {
		var polls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if polls.Add(1) < 3 {
				http.NotFound(w, nil)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"proof":"fixture"}`)
		}))
		defer server.Close()
		poll := func(ctx context.Context, id string) (bool, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/settlements/"+id, nil)
			if err != nil {
				return false, err
			}
			res, err := server.Client().Do(req)
			if err != nil {
				return false, err
			}
			defer res.Body.Close()
			if res.StatusCode == http.StatusNotFound {
				return false, errNotFound
			}
			return res.StatusCode == http.StatusOK, nil
		}
		if err := waitForReceipts(context.Background(), []string{"receipt-fixed"}, time.Millisecond, poll); err != nil {
			t.Fatal(err)
		}
		if got := polls.Load(); got != 3 {
			t.Fatalf("poll count = %d; want 3", got)
		}
	})
	t.Run("deadline identifies pending receipt", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
		defer cancel()
		err := waitForReceipts(ctx, []string{"receipt-pending"}, time.Second, func(context.Context, string) (bool, error) { return false, errNotFound })
		if err == nil || !strings.Contains(err.Error(), "receipt-pending") {
			t.Fatalf("wait error = %v; want receipt-pending", err)
		}
	})
}
