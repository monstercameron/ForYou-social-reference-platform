package delegation

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestFixtureDelegation(t *testing.T) {
	data, err := os.ReadFile("testdata/delegation-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Delegation wire.Delegation `json:"delegation"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	d := fixture.Delegation
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	use := Use{AuthorID: d.AuthorID, SignerID: d.SignerID, MessageType: "post", ProviderID: d.ProviderID, At: at}
	t.Run("fixture IDs and signature verify", func(t *testing.T) {
		if got, err := EpochID(d.Epoch); err != nil || got != d.Epoch.EpochID {
			t.Fatalf("EpochID = %q, %v", got, err)
		}
		if got, err := DelegationID(d); err != nil || got != d.DelegationID {
			t.Fatalf("DelegationID = %q, %v", got, err)
		}
		if err := VerifyDelegation(d, use); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("changed epoch sequence rejected", func(t *testing.T) {
		changed := d
		changed.Epoch.EpochSequence = 2
		if err := VerifyDelegation(changed, use); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
	t.Run("arbitrary delegation ID rejected", func(t *testing.T) {
		changed := d
		changed.DelegationID = "fyd1:" + strings.Repeat("0", 64)
		if err := VerifyDelegation(changed, use); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
	t.Run("bad root signature rejected", func(t *testing.T) {
		changed := d
		changed.DelegationSignature = keys.EncodeB64URL(make([]byte, 64))
		if err := VerifyDelegation(changed, use); !errors.Is(err, ErrBadRootSignature) {
			t.Fatalf("VerifyDelegation error = %v; want ErrBadRootSignature", err)
		}
	})
	t.Run("missing epoch rejected", func(t *testing.T) {
		changed := d
		changed.Epoch = wire.AuthorizationEpoch{}
		if err := VerifyDelegation(changed, use); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
	t.Run("wrong provider rejected", func(t *testing.T) {
		changed := use
		changed.ProviderID = d.AuthorID
		if err := VerifyDelegation(d, changed); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
	t.Run("wrong signer rejected", func(t *testing.T) {
		changed := use
		changed.SignerID = d.AuthorID
		if err := VerifyDelegation(d, changed); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
	t.Run("validity is half open", func(t *testing.T) {
		changed := use
		changed.At = time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC)
		if err := VerifyDelegation(d, changed); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
	t.Run("time before valid_from rejected", func(t *testing.T) {
		changed := use
		changed.At = time.Date(2026, 11, 10, 22, 59, 59, 0, time.UTC)
		if err := VerifyDelegation(d, changed); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
	t.Run("lifetime boundary accepted and longer lifetime rejected", func(t *testing.T) {
		seed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
		root, err := keys.NewKeyFromSeed(seed)
		if err != nil {
			t.Fatal(err)
		}
		boundary := d
		boundary.ValidUntil = "2026-11-11T23:00:00Z"
		boundary, err = SignDelegation(root, boundary)
		if err != nil {
			t.Fatal(err)
		}
		boundaryUse := use
		boundaryUse.At = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		if err = VerifyDelegation(boundary, boundaryUse); err != nil {
			t.Fatalf("24-hour boundary rejected: %v", err)
		}
		long := d
		long.ValidUntil = "2026-11-11T23:00:01Z"
		long, err = SignDelegation(root, long)
		if err != nil {
			t.Fatal(err)
		}
		if err = VerifyDelegation(long, use); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
	t.Run("oversized canonical delegation rejected", func(t *testing.T) {
		large := d
		large.SignerID = "did:key:" + strings.Repeat("x", MaxDelegationBytes)
		if err := VerifyDelegation(large, use); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("VerifyDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
}

func TestStateResponseChallengeBinding(t *testing.T) {
	seed := []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	provider := keys.DIDOf(key)
	state := wire.AuthorizationEpochState{ProviderID: provider, AuthorID: provider, StateRevision: 0, Status: "unregistered"}
	body, err := canon.EncodeWithout(state, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	state.ProviderSignature, err = keys.Sign(key, "authorization-epoch-state", body)
	if err != nil {
		t.Fatal(err)
	}
	response := wire.AuthorizationEpochStateResponse{Challenge: keys.EncodeB64URL(make([]byte, 32)), State: state}
	body, err = canon.EncodeWithout(response, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	response.ProviderSignature, err = keys.Sign(key, "authorization-epoch-state-response", body)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("matching challenge verifies", func(t *testing.T) {
		if err := VerifyStateResponse(response, provider, response.Challenge); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("different challenge fails", func(t *testing.T) {
		if err := VerifyStateResponse(response, provider, keys.EncodeB64URL([]byte("12345678901234567890123456789012"))); err == nil {
			t.Fatal("accepted different challenge")
		}
	})
}

func TestCheckIntentDelegation(t *testing.T) {
	data, err := os.ReadFile("testdata/delegation-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Delegation wire.Delegation `json:"delegation"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	d := fixture.Delegation
	intent := wire.Intent{AuthorID: d.AuthorID, SignerID: d.SignerID, MessageType: "post", Delegation: &d}
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	t.Run("matching intent verifies", func(t *testing.T) {
		if err := CheckIntentDelegation(d, intent, d.ProviderID, at); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("different embedded delegation rejected", func(t *testing.T) {
		changed := d
		changed.DelegationID = "fyd1:" + strings.Repeat("0", 64)
		intent.Delegation = &changed
		if err := CheckIntentDelegation(d, intent, d.ProviderID, at); !errors.Is(err, ErrInvalidDelegation) {
			t.Fatalf("CheckIntentDelegation error = %v; want ErrInvalidDelegation", err)
		}
	})
}
