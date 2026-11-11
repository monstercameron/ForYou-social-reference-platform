package monitor

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

type depositSource struct {
	chain faux.Chain
	view  *View
}

func (d *depositSource) Deposited(firstKey, network string) (*big.Int, error) {
	commitment, err := settlement.DepositCommitment(firstKey, network)
	if err != nil {
		return nil, err
	}
	transfers := make([]settlement.Transfer, 0)
	var after uint64
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		page, err := d.chain.Transfers(ctx, faux.Query{Network: network, Data: hex.EncodeToString(commitment[:]), After: after, Limit: 1000})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("monitor: list faux transfers: %w", err)
		}
		for _, item := range page {
			amount, ok := new(big.Int).SetString(item.AmountChainUnits, 10)
			blockTime, timeErr := wire.ParseTime(item.BlockTime)
			data, dataErr := hex.DecodeString(item.Data)
			if !ok || timeErr != nil || dataErr != nil {
				return nil, fmt.Errorf("monitor: invalid faux transfer encoding")
			}
			transfers = append(transfers, settlement.Transfer{Network: item.Network, Recipient: item.Recipient, ChainUnits: amount, DataOutputs: [][]byte{data}, Final: item.Finalized, Successful: item.Success, BlockTime: blockTime.UTC()})
			after = item.Sequence
		}
		if len(page) < 1000 {
			break
		}
	}
	trust := d.view.Trust()
	return settlement.CountDeposits(transfers, firstKey, network, trust.RecipientCounts)
}

var _ committee.DepositSource = (*depositSource)(nil)
