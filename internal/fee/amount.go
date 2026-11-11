package fee

import (
	"errors"
	"fmt"
	"math/big"

	"foryou.local/foryou/internal/wire"
)

const (
	// BitcoinMainnet is the Bitcoin mainnet identifier.
	BitcoinMainnet = wire.BitcoinMainnet
	// EthereumMainnet is the Ethereum mainnet identifier.
	EthereumMainnet = wire.EthereumMainnet
	// MaxBillableBytes is the protocol's maximum quoted intent size.
	MaxBillableBytes = 1048576
	// CustodyCapPosts is the number of 1 KiB fees defining the custody cap.
	CustodyCapPosts = 1000000
)

var (
	// ErrUnsupportedNetwork identifies a network outside the supported mainnets.
	ErrUnsupportedNetwork = errors.New("fee: unsupported network")
	// ErrInvalidAmount identifies a malformed or negative base-unit amount.
	ErrInvalidAmount = errors.New("fee: invalid amount")
)

// ParseAmount parses a canonical nonnegative base-unit decimal amount.
func ParseAmount(s string) (*big.Int, error) {
	n, err := wire.Amount(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidAmount, err)
	}
	return n, nil
}

// ParsePositiveAmount parses a canonical positive base-unit decimal amount.
func ParsePositiveAmount(s string) (*big.Int, error) {
	n, err := wire.PositiveAmount(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidAmount, err)
	}
	return n, nil
}

// FormatAmount formats a nonnegative integer as a base-unit decimal string.
func FormatAmount(v *big.Int) string {
	if v == nil || v.Sign() < 0 {
		return ""
	}
	return v.String()
}

// Denomination returns the accounting unit for a supported network.
func Denomination(network string) (string, error) {
	switch network {
	case BitcoinMainnet:
		return "millisatoshi", nil
	case EthereumMainnet:
		return "wei", nil
	default:
		return "", ErrUnsupportedNetwork
	}
}

// AccountingUnits converts chain units to exact accounting units.
func AccountingUnits(network string, chainUnits *big.Int) (*big.Int, error) {
	if _, err := Denomination(network); err != nil {
		return nil, err
	}
	if chainUnits == nil || chainUnits.Sign() < 0 {
		return nil, ErrInvalidAmount
	}
	if network == BitcoinMainnet {
		return new(big.Int).Mul(chainUnits, big.NewInt(1000)), nil
	}
	return new(big.Int).Set(chainUnits), nil
}

// Amount calculates the fee for a supported billable byte count.
func Amount(s wire.FeeSchedule, billableBytes int) (*big.Int, error) {
	if _, err := Denomination(s.Network); err != nil {
		return nil, err
	}
	if billableBytes < 1 || billableBytes > MaxBillableBytes {
		return nil, fmt.Errorf("fee: billable_bytes out of range")
	}
	base, err := ParseAmount(s.BaseFeeBaseUnits)
	if err != nil {
		return nil, err
	}
	rate, err := ParseAmount(s.PerKiBFeeBaseUnits)
	if err != nil {
		return nil, err
	}
	kib := (billableBytes + 1023) / 1024
	return new(big.Int).Add(base, new(big.Int).Mul(rate, big.NewInt(int64(kib)))), nil
}

// CustodyCap returns one million times the schedule's 1 KiB fee.
func CustodyCap(s wire.FeeSchedule) (*big.Int, error) {
	n, err := Amount(s, 1024)
	if err != nil {
		return nil, err
	}
	return n.Mul(n, big.NewInt(CustodyCapPosts)), nil
}

// DepositAllowed reports whether a proposed deposit remains within the custody cap.
func DepositAllowed(s wire.FeeSchedule, confirmed, pending, amount *big.Int) (bool, error) {
	limit, err := CustodyCap(s)
	if err != nil {
		return false, err
	}
	if confirmed == nil || pending == nil || amount == nil || confirmed.Sign() < 0 || pending.Sign() < 0 || amount.Sign() < 0 {
		return false, ErrInvalidAmount
	}
	total := new(big.Int).Add(confirmed, pending)
	total.Add(total, amount)
	return total.Cmp(limit) <= 0, nil
}
