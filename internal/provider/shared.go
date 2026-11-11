package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

type epochStore struct{ store store.Store }

func (s *Service) initializeShared() error {
	root, err := governance.CheckGenesis(s.cfg.Genesis)
	if err != nil {
		return err
	}
	for _, policy := range s.cfg.Policies {
		if err := distribution.CheckPolicy(policy, s.policies, s.cfg.Trust); err != nil {
			return err
		}
		s.policies = append(s.policies, clonePolicy(policy))
	}
	approved := append([]wire.ApprovedFeeSchedule(nil), s.cfg.FeeSchedules...)
	sort.Slice(approved, func(i, j int) bool {
		if approved[i].Schedule.Network != approved[j].Schedule.Network {
			return approved[i].Schedule.Network < approved[j].Schedule.Network
		}
		return approved[i].Schedule.Revision < approved[j].Schedule.Revision
	})
	if len(approved) == 0 {
		return fmt.Errorf("provider: no fee schedules")
	}
	for _, a := range approved {
		if err := s.verifySchedule(a, root.IDs, root.Threshold); err != nil {
			return err
		}
		s.schedules = append(s.schedules, a.Schedule)
	}
	if err := fee.CheckScheduleSet(s.schedules); err != nil {
		return err
	}
	if s.cfg.DataDir != "" {
		s.fundingJournal, err = funding.NewFileJournal(filepath.Join(s.cfg.DataDir, "funding", "journal.jsonl"))
		if err != nil {
			return err
		}
	}
	s.Ledger, err = funding.Open(funding.Config{ProviderID: s.ProviderID(), Journal: s.fundingJournal, Now: s.Now, Rand: s.cfg.Random, RecipientLabel: s.cfg.RecipientLabel})
	if err != nil {
		return err
	}
	s.ServiceAuth, err = delegation.NewServiceAuth(delegation.ServiceAuthConfig{ServiceID: s.ProviderID(), Store: delegation.NewMemoryChallengeStore(), Now: s.Now, Random: s.cfg.Random, Limits: delegation.Limits{MaxOutstanding: 10000, MaxPerSource: 60, ReservedProtected: 100}})
	if err != nil {
		return err
	}
	s.Authority, err = delegation.NewAuthority(delegation.AuthorityConfig{ProviderKey: s.cfg.Key, Store: epochStore{s.controls}, Now: s.Now, Random: s.cfg.Random, MaxRevocationsPerAuthor: 10000})
	if err != nil {
		return err
	}
	s.Successions = succession.NewRegistry(16)
	if err := s.controls.View(func(tx store.Tx) error {
		clock, _, err := tx.Get("provider.core.clock", "high_water")
		if err != nil {
			return err
		}
		if len(clock) > 0 {
			s.highWater, err = time.Parse(time.RFC3339, string(clock))
		}
		return err
	}); err != nil {
		return err
	}
	return s.controls.View(func(tx store.Tx) error {
		snapshot, ok, err := tx.Get("provider.successions", "snapshot")
		if err != nil || !ok || len(snapshot) == 0 {
			return err
		}
		s.Successions, err = succession.RestoreRegistry(snapshot, 16)
		return err
	})
}

func (s *Service) verifySchedule(a wire.ApprovedFeeSchedule, releaseIDs []string, threshold int) error {
	var previous *wire.FeeSchedule
	for i := range s.schedules {
		old := s.schedules[i]
		if old.Network == a.Schedule.Network && old.Revision < a.Schedule.Revision && (previous == nil || old.Revision > previous.Revision) {
			v := old
			previous = &v
		}
	}
	if err := fee.VerifyApproval(a.Schedule, a.Approval, releaseIDs, threshold, previous); err != nil {
		return err
	}
	return nil
}

func (e epochStore) Load(id string) (delegation.AuthorRecord, error) {
	var out delegation.AuthorRecord
	err := e.store.View(func(tx store.Tx) error {
		b, ok, err := tx.Get("provider.delegation.authors", id)
		if err != nil {
			return err
		}
		if !ok {
			out = delegation.AuthorRecord{State: wire.AuthorizationEpochState{AuthorID: id, Status: "unregistered"}}
			return nil
		}
		return json.Unmarshal(b, &out)
	})
	return out, err
}
func (e epochStore) Commit(id string, prev uint64, next delegation.AuthorRecord) error {
	return e.store.Update(func(tx store.Tx) error {
		b, ok, err := tx.Get("provider.delegation.authors", id)
		if err != nil {
			return err
		}
		version := uint64(0)
		if ok {
			var current delegation.AuthorRecord
			if err = json.Unmarshal(b, &current); err != nil {
				return err
			}
			version = current.Version
		}
		if version != prev {
			return delegation.ErrStale
		}
		if next.Version != prev+1 {
			return errors.New("provider: invalid author version")
		}
		b, err = json.Marshal(next)
		if err != nil {
			return err
		}
		return tx.Put("provider.delegation.authors", id, b)
	})
}
