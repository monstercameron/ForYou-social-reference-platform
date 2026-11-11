package foundation

import (
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"time"

	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func (s *Service) vaultStatusPublish(end time.Time) error {
	periodEnd := wire.FormatTime(end)
	if _, exists := s.VaultStatus(periodEnd); exists {
		return nil
	}
	for _, network := range []string{wire.BitcoinMainnet, wire.EthereumMainnet} {
		if _, exists := s.DistributionStatement(network, periodEnd); !exists {
			return nil
		}
	}
	start, _ := s.Calendar().Quarter(end.Add(-time.Nanosecond))
	status := wire.VaultStatus{FoundationID: keys.DIDOf(s.cfg.Key), PeriodStart: wire.FormatTime(start), PeriodEnd: periodEnd, Funding: []wire.VaultFunding{}}
	// Record totals are cumulative through period_end and use the stored message JCS byte lengths.
	for _, record := range s.VaultRecords() {
		storedAt, err := wire.ParseTime(record.Receipt.StoredAt)
		if err != nil {
			return err
		}
		if !storedAt.Before(end) {
			continue
		}
		status.RecordCount++
		status.StoredBytes += record.StoredSize
	}
	for _, network := range []string{wire.BitcoinMainnet, wire.EthereumMainnet} {
		data, _ := s.DistributionStatement(network, periodEnd)
		var statement wire.DistributionStatement
		if err := json.Unmarshal(data, &statement); err != nil {
			return err
		}
		received, ok := new(big.Int).SetString(statement.ReceivedBaseUnits, 10)
		if !ok {
			return errors.New("foundation: invalid statement received amount")
		}
		distributed, ok := new(big.Int).SetString(statement.DistributedBaseUnits, 10)
		if !ok {
			return errors.New("foundation: invalid statement distributed amount")
		}
		seats := new(big.Int)
		for _, payment := range statement.MonitorPayments {
			if payment.Payout == nil {
				continue
			}
			amount, ok := new(big.Int).SetString(payment.AmountBaseUnits, 10)
			if !ok {
				return errors.New("foundation: invalid monitor payment amount")
			}
			seats.Add(seats, amount)
		}
		kept := new(big.Int).Sub(received, distributed)
		kept.Sub(kept, seats)
		if kept.Sign() < 0 {
			return errors.New("foundation: statement spends more than received")
		}
		// Received is fees kept after actual provider transfers and paid seats; no operating-spend ledger exists yet.
		// Spent remains zero until the Foundation has a recorded operating-spend ledger.
		status.Funding = append(status.Funding, wire.VaultFunding{Network: network, ReceivedBaseUnits: kept.String(), SpentBaseUnits: "0"})
	}
	sort.Slice(status.Funding, func(i, j int) bool { return status.Funding[i].Network < status.Funding[j].Network })
	_, _, err := s.SignAndLog(governance.LogKindVaultStatus, "vault-status", status)
	return err
}
