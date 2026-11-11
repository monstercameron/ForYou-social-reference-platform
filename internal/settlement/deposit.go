package settlement

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// Transfer is a chain transfer adapted by the caller for settlement deposit counting.
type Transfer struct {
	Network          string
	Recipient        string
	ChainUnits       *big.Int
	DataOutputs      [][]byte
	RecipientHasCode bool
	Final            bool
	Successful       bool
	BlockTime        time.Time
}

// RecipientCounts reports whether a verified Foundation recipient was valid at block time.
type RecipientCounts func(network, address string, blockTime time.Time) bool

// DepositCommitment returns the provider and network commitment carried by counted deposits.
func DepositCommitment(firstKey, network string) ([32]byte, error) {
	if err := checkClaimNetwork(network); err != nil {
		return [32]byte{}, err
	}
	canonical, err := canon.Encode(struct {
		ProviderID string `json:"provider_id"`
		Network    string `json:"network"`
	}{ProviderID: firstKey, Network: network})
	if err != nil {
		return [32]byte{}, err
	}
	input, err := keys.SigningInput("settlement-deposit", canonical)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(input), nil
}

// MinimumDeposit returns the minimum qualifying transfer in chain units.
func MinimumDeposit(network string) (*big.Int, error) {
	switch network {
	case wire.BitcoinMainnet:
		return big.NewInt(10000), nil
	case wire.EthereumMainnet:
		return new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil), nil
	default:
		return nil, unsupportedClaimNetwork(network)
	}
}

// BaseUnits converts satoshis to millisatoshis or leaves wei unchanged.
func BaseUnits(network string, chainUnits *big.Int) (*big.Int, error) {
	if err := checkClaimNetwork(network); err != nil {
		return nil, err
	}
	if chainUnits == nil || chainUnits.Sign() < 0 {
		return nil, errors.New("settlement: invalid chain amount")
	}
	if network == wire.BitcoinMainnet {
		return new(big.Int).Mul(new(big.Int).Set(chainUnits), big.NewInt(1000)), nil
	}
	return new(big.Int).Set(chainUnits), nil
}

// CountDeposits sums transfers that carry firstKey's commitment to an accepted Foundation recipient.
func CountDeposits(ts []Transfer, firstKey, network string, recipient RecipientCounts) (*big.Int, error) {
	commitment, err := DepositCommitment(firstKey, network)
	if err != nil {
		return nil, err
	}
	minimum, err := MinimumDeposit(network)
	if err != nil {
		return nil, err
	}
	total := new(big.Int)
	for _, transfer := range ts {
		if transfer.Network != network || !transfer.Final || !transfer.Successful || transfer.ChainUnits == nil || transfer.ChainUnits.Sign() < 0 || transfer.ChainUnits.Cmp(minimum) < 0 || recipient == nil || !recipient(network, transfer.Recipient, transfer.BlockTime) {
			continue
		}
		if network == wire.EthereumMainnet && transfer.RecipientHasCode {
			continue
		}
		if len(transfer.DataOutputs) != 1 || !bytes.Equal(transfer.DataOutputs[0], commitment[:]) {
			continue
		}
		amount, err := BaseUnits(network, transfer.ChainUnits)
		if err != nil {
			return nil, fmt.Errorf("settlement: convert transfer amount: %w", err)
		}
		total.Add(total, amount)
	}
	return total, nil
}

// Credit returns the remaining settlement deposit after a claim's cumulative total.
func Credit(deposited *big.Int, c wire.SettlementClaim) (*big.Int, error) {
	if deposited == nil || deposited.Sign() < 0 {
		return nil, errors.New("settlement: invalid deposited amount")
	}
	cumulative, err := wire.PositiveAmount(c.CumulativeBaseUnits)
	if err != nil {
		return nil, fmt.Errorf("settlement: invalid claim cumulative amount: %w", err)
	}
	return new(big.Int).Sub(new(big.Int).Set(deposited), cumulative), nil
}
