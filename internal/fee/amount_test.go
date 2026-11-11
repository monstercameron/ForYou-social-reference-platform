package fee

import (
	"math/big"
	"strings"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func TestScheduleOneAmounts(t *testing.T) {
	s := wire.FeeSchedule{Network: BitcoinMainnet, BaseFeeBaseUnits: "0", PerKiBFeeBaseUnits: "136"}
	check := func(t *testing.T, n int, want string) {
		t.Helper()
		got, err := Amount(s, n)
		if err != nil || got.String() != want {
			t.Fatalf("Amount(%d) = %v, %v; want %s", n, got, err, want)
		}
	}
	t.Run("one byte", func(t *testing.T) { check(t, 1, "136") })
	t.Run("one kibibyte", func(t *testing.T) { check(t, 1024, "136") })
	t.Run("first byte of second kibibyte", func(t *testing.T) { check(t, 1025, "272") })
	t.Run("four kibibytes", func(t *testing.T) { check(t, 4096, "544") })
	t.Run("first byte of fifth kibibyte", func(t *testing.T) { check(t, 4097, "680") })
	t.Run("maximum", func(t *testing.T) { check(t, MaxBillableBytes, "139264") })
	t.Run("zero rejected", func(t *testing.T) {
		if _, err := Amount(s, 0); err == nil {
			t.Fatal("Amount accepted zero bytes")
		}
	})
	t.Run("over maximum rejected", func(t *testing.T) {
		if _, err := Amount(s, MaxBillableBytes+1); err == nil {
			t.Fatal("Amount accepted oversized input")
		}
	})
	t.Run("ethereum 1025 bytes", func(t *testing.T) {
		s.Network = EthereumMainnet
		s.PerKiBFeeBaseUnits = "48900000000"
		got, err := Amount(s, 1025)
		if err != nil || got.String() != "97800000000" {
			t.Fatalf("Amount = %v, %v", got, err)
		}
	})
}

func TestParseAmount(t *testing.T) {
	checkBad := func(t *testing.T, v string) {
		t.Helper()
		if _, err := ParseAmount(v); err == nil {
			t.Fatalf("ParseAmount(%q) accepted", v)
		}
	}
	t.Run("leading zero", func(t *testing.T) { checkBad(t, "01") })
	t.Run("negative", func(t *testing.T) { checkBad(t, "-1") })
	t.Run("positive sign", func(t *testing.T) { checkBad(t, "+1") })
	t.Run("whitespace", func(t *testing.T) { checkBad(t, " 1") })
	t.Run("exponent", func(t *testing.T) { checkBad(t, "1e3") })
	t.Run("seventy nine digits", func(t *testing.T) {
		checkBad(t, "1"+strings.Repeat("0", 78))
	})
	t.Run("zero", func(t *testing.T) {
		got, err := ParseAmount("0")
		if err != nil || got.Sign() != 0 {
			t.Fatalf("ParseAmount zero = %v, %v", got, err)
		}
	})
	t.Run("seventy eight digits", func(t *testing.T) {
		v := "1" + strings.Repeat("0", 77)
		got, err := ParseAmount(v)
		if err != nil || got.String() != v {
			t.Fatalf("ParseAmount = %v, %v", got, err)
		}
	})
}

func TestAccountingUnits(t *testing.T) {
	t.Run("satoshis to millisatoshis", func(t *testing.T) {
		got, err := AccountingUnits(BitcoinMainnet, big.NewInt(12))
		if err != nil || got.String() != "12000" {
			t.Fatalf("AccountingUnits = %v, %v", got, err)
		}
	})
	t.Run("wei unchanged", func(t *testing.T) {
		got, err := AccountingUnits(EthereumMainnet, big.NewInt(12))
		if err != nil || got.String() != "12" {
			t.Fatalf("AccountingUnits = %v, %v", got, err)
		}
	})
	t.Run("unknown network rejected", func(t *testing.T) {
		if _, err := AccountingUnits("bitcoin-testnet", big.NewInt(12)); err == nil {
			t.Fatal("unsupported network accepted")
		}
	})
}
