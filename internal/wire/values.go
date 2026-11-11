package wire

import (
	"errors"
	"math/big"
	"regexp"
	"time"
)

const (
	// BitcoinMainnet is the Bitcoin mainnet network identifier.
	BitcoinMainnet = "bitcoin-mainnet"
	// EthereumMainnet is the Ethereum mainnet network identifier.
	EthereumMainnet = "ethereum-mainnet"
)

var (
	timePattern   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
	datePattern   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
)

// ParseTime parses the protocol's strict UTC second timestamp.
func ParseTime(s string) (time.Time, error) {
	if !timePattern.MatchString(s) || s[:4] == "0000" {
		return time.Time{}, errors.New("invalid protocol time")
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", s)
	if err != nil {
		return time.Time{}, errors.New("invalid protocol time")
	}
	return t.UTC(), nil
}

// FormatTime formats t as a UTC timestamp with whole-second precision.
func FormatTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
}

// ParseDate parses a strict Gregorian calendar date as UTC midnight.
func ParseDate(s string) (time.Time, error) {
	if !datePattern.MatchString(s) || s[:4] == "0000" {
		return time.Time{}, errors.New("invalid protocol date")
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, errors.New("invalid protocol date")
	}
	return t.UTC(), nil
}

// Amount parses a canonical nonnegative base-unit decimal integer of at most 78 digits.
func Amount(s string) (*big.Int, error) {
	if len(s) > 78 || !amountPattern.MatchString(s) {
		return nil, errors.New("invalid amount")
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, errors.New("invalid amount")
	}
	return n, nil
}

// PositiveAmount parses a canonical positive base-unit decimal integer of at most 78 digits.
func PositiveAmount(s string) (*big.Int, error) {
	n, err := Amount(s)
	if err != nil || n.Sign() == 0 {
		return nil, errors.New("invalid positive amount")
	}
	return n, nil
}
