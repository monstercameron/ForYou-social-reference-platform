package monitor

import (
	"context"
	"log"
	"sort"

	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
)

func init() {
	loopSteps = append(loopSteps, loopStep{order: trustRefreshOrder, name: "trust refresh", run: alRefreshTrust})
}

// alFoundationID is the identity a Foundation client is pinned to: the key in
// force now, or the genesis key when none is. A closed key without a successor
// is exactly when the Foundation must still be read, so this never gives up
// while the genesis names a key.
func alFoundationID(s *Service) (string, bool) {
	id, ok := s.View().Trust().CurrentFoundationID(s.Now())
	if !ok {
		certificates := s.config.Genesis.FoundationKeyCertificates
		if len(certificates) != 0 {
			id = certificates[0].FoundationID
		}
	}
	_, err := keys.ParseDID(id)
	return id, err == nil
}

func alRefreshTrust(s *Service, ctx context.Context) error {
	base := s.VaultURL()
	if base == "" {
		return nil
	}
	id, ok := alFoundationID(s)
	if !ok {
		log.Printf("monitor: trust refresh skipped: no current Foundation did:key")
		return nil
	}
	client, err := sdk.New(base, sdk.Options{ServiceID: id, AllowLoopbackHTTP: true, HTTPClient: s.HTTPClient(), Now: s.config.Now})
	if err != nil {
		log.Printf("monitor: trust refresh client: %v", err)
		return nil
	}
	page, _, err := client.Certificates(ctx)
	if err != nil {
		log.Printf("monitor: fetch Foundation certificates: %v", err)
		return nil
	}
	trust, err := governance.NewTrust(s.config.Genesis, page.KeySetUpdates, page.Certificates, page.Closures, nil)
	if err != nil {
		log.Printf("monitor: verify Foundation certificates: %v", err)
		return nil
	}
	s.View().SetTrust(trust)
	schedules, _, err := client.FeeSchedules(ctx)
	if err != nil {
		log.Printf("monitor: fetch Foundation fee schedules: %v", err)
	} else {
		for _, schedule := range schedules.Schedules {
			if err := s.View().AddSchedule(schedule); err != nil {
				log.Printf("monitor: reject Foundation fee schedule %s: %v", schedule.Schedule.ScheduleID, err)
			}
		}
	}
	distribution, _, err := client.Distribution(ctx)
	if err != nil {
		log.Printf("monitor: fetch Foundation distribution: %v", err)
		return nil
	}
	sort.SliceStable(distribution.Policies, func(i, j int) bool {
		return distribution.Policies[i].EffectiveAt < distribution.Policies[j].EffectiveAt
	})
	for _, policy := range distribution.Policies {
		if err := s.View().AddPolicy(policy); err != nil {
			log.Printf("monitor: reject Foundation distribution policy %s: %v", policy.PolicyID, err)
		}
	}
	return nil
}
