package wire

import (
	"math/big"
	"testing"
	"time"
)

func TestSchemaValues(t *testing.T) {
	t.Run("strict valid times", func(t *testing.T) {
		if _, err := ParseTime("2026-11-11T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if _, err := ParseTime("2028-02-29T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("invalid times", func(t *testing.T) {
		if _, err := ParseTime("2026-02-29T00:00:00Z"); err == nil {
			t.Fatal("accepted invalid leap day")
		}
		if _, err := ParseTime("2026-11-11T23:59:60Z"); err == nil {
			t.Fatal("accepted leap second")
		}
		if _, err := ParseTime("2026-11-11T00:00:00z"); err == nil {
			t.Fatal("accepted lowercase zone")
		}
		if _, err := ParseTime("2026-11-11T00:00:00.5Z"); err == nil {
			t.Fatal("accepted fractional second")
		}
		if _, err := ParseTime("2026-11-11T00:00:00+00:00"); err == nil {
			t.Fatal("accepted offset zone")
		}
	})
	t.Run("format time converts and truncates", func(t *testing.T) {
		input := time.Date(2026, 11, 11, 1, 2, 3, 987654321, time.FixedZone("offset", 3600))
		if got := FormatTime(input); got != "2026-11-11T00:02:03Z" {
			t.Fatalf("FormatTime = %q", got)
		}
	})
	t.Run("strict date", func(t *testing.T) {
		if _, err := ParseDate("2028-02-29"); err != nil {
			t.Fatal(err)
		}
		if _, err := ParseDate("2026-02-29"); err == nil {
			t.Fatal("accepted invalid date")
		}
	})
	t.Run("amount boundaries", func(t *testing.T) {
		if got, err := Amount("0"); err != nil || got.Cmp(big.NewInt(0)) != 0 {
			t.Fatalf("Amount(0) = %v, %v", got, err)
		}
		if _, err := Amount("999999999999999999999999999999999999999999999999999999999999999999999999999999"); err != nil {
			t.Fatal(err)
		}
		if _, err := Amount("01"); err == nil {
			t.Fatal("accepted leading zero")
		}
		if _, err := Amount("-1"); err == nil {
			t.Fatal("accepted negative amount")
		}
		if _, err := Amount("9999999999999999999999999999999999999999999999999999999999999999999999999999999"); err == nil {
			t.Fatal("accepted 79 digits")
		}
		if _, err := PositiveAmount("0"); err == nil {
			t.Fatal("accepted zero positive amount")
		}
	})
}
