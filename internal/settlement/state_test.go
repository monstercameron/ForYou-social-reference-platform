package settlement

import (
	"testing"
	"time"

	"foryou.local/foryou/internal/wire"
)

func TestPendingStateAndDelinquent(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2027-02-01T00:00:00Z")
	input := func(issued string) PendingInput {
		return PendingInput{Receipt: wire.SubmissionReceipt{ProviderID: "successor-key", ReceiptID: "receipt-1", IssuedAt: issued}, FromProviderRoute: true, TrustedProvider: true, ProviderLine: "first-key"}
	}
	t.Run("trusted receipt before seven days is pending", func(t *testing.T) {
		in := input("2027-01-25T01:00:00Z")
		if got := PendingState(in, nil, now); got != StatePending {
			t.Fatalf("PendingState() = %q; want %q", got, StatePending)
		}
		if _, ok := Delinquent(in, now); ok {
			t.Fatal("recent receipt is delinquent")
		}
	})
	t.Run("seven days is delinquent", func(t *testing.T) {
		in := input("2027-01-25T00:00:00Z")
		if got := PendingState(in, nil, now); got != StateUnsettled {
			t.Fatalf("PendingState() = %q; want unsettled", got)
		}
		got, ok := Delinquent(in, now)
		if !ok || got != (DelinquencyRecord{ProviderLine: "first-key", ReceiptID: "receipt-1"}) {
			t.Fatalf("Delinquent() = %#v, %v", got, ok)
		}
	})
	t.Run("untrusted provider is unsettled", func(t *testing.T) {
		in := input("2027-01-31T00:00:00Z")
		in.TrustedProvider = false
		if got := PendingState(in, nil, now); got != StateUnsettled {
			t.Fatalf("PendingState() = %q; want unsettled", got)
		}
	})
	t.Run("other route is neither pending nor delinquent", func(t *testing.T) {
		in := input("2027-01-01T00:00:00Z")
		in.FromProviderRoute = false
		if got := PendingState(in, nil, now); got != StateUnsettled {
			t.Fatalf("PendingState() = %q; want unsettled", got)
		}
		if _, ok := Delinquent(in, now); ok {
			t.Fatal("other-route receipt is delinquent")
		}
	})
	t.Run("future receipt is unsettled", func(t *testing.T) {
		if got := PendingState(input("2027-02-01T00:00:01Z"), nil, now); got != StateUnsettled {
			t.Fatalf("PendingState() = %q; want unsettled", got)
		}
	})
	t.Run("record blocks successor key on same line", func(t *testing.T) {
		in := input("2027-01-31T00:00:00Z")
		records := map[DelinquencyRecord]bool{{ProviderLine: "first-key", ReceiptID: "old-receipt"}: true}
		if got := PendingState(in, records, now); got != StateUnsettled {
			t.Fatalf("PendingState() = %q; want unsettled", got)
		}
	})
}
