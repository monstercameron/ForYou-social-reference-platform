package fee

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestReceiptVector(t *testing.T) {
	v := readFeeExample(t)
	r, q := v.Message.SubmissionReceipt, v.Quotes[0]
	t.Run("example verifies and binds quote", func(t *testing.T) {
		if err := VerifyReceipt(r); err != nil {
			t.Fatal(err)
		}
		if err := CheckReceipt(r, q); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sign reproduces example", func(t *testing.T) {
		got, err := SignReceipt(r, exampleProviderKey(t))
		if err != nil || got.ProviderSignature != r.ProviderSignature {
			t.Fatalf("SignReceipt signature = %s, %v", got.ProviderSignature, err)
		}
	})
	t.Run("changed amount fails quote binding", func(t *testing.T) {
		bad := r
		bad.AmountBaseUnits = "3"
		bad, err := SignReceipt(bad, exampleProviderKey(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckReceipt(bad, q); !errors.Is(err, ErrQuoteMismatch) {
			t.Fatalf("CheckReceipt error = %v; want ErrQuoteMismatch", err)
		}
	})
	t.Run("noncanonical receipt ID rejected", func(t *testing.T) {
		bad := r
		bad.ReceiptID = bad.ReceiptID[:len(bad.ReceiptID)-1] + "N"
		if err := VerifyReceipt(bad); err == nil {
			t.Fatal("noncanonical receipt ID accepted")
		}
	})
	t.Run("authorization time must equal issue time", func(t *testing.T) {
		bad := r
		bad.AuthorizationAt = "2026-11-11T00:00:05Z"
		if VerifyReceipt(bad) == nil {
			t.Fatal("mismatched authorization time accepted")
		}
	})
	t.Run("sign refuses mismatched authorization time", func(t *testing.T) {
		bad := r
		bad.AuthorizationAt = "2026-11-11T00:00:05Z"
		if got, err := SignReceipt(bad, exampleProviderKey(t)); err == nil || got.ProviderSignature != "" {
			t.Fatalf("SignReceipt = %q, %v; want no signature and an error", got.ProviderSignature, err)
		}
	})
}

func TestNewReceiptID(t *testing.T) {
	t.Run("zero bytes produce canonical ID", func(t *testing.T) {
		got, err := NewReceiptID(bytes.NewReader(make([]byte, 32)))
		if err != nil || got != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
			t.Fatalf("NewReceiptID = %q, %v", got, err)
		}
	})
	t.Run("short random input rejected", func(t *testing.T) {
		_, err := NewReceiptID(bytes.NewReader([]byte{1, 2}))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("NewReceiptID error = %v; want io.ErrUnexpectedEOF", err)
		}
	})
}
