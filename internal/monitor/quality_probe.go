package monitor

import (
	"context"
	"errors"
	"net/http"
	"time"

	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/quality"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

// qualityStatusTransport remembers the HTTP status of a response the sdk later rejects as invalid.
type qualityStatusTransport struct {
	base   http.RoundTripper
	status int
}

func (t *qualityStatusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if response != nil {
		t.status = response.StatusCode
	}
	return response, err
}

// qualityExecuteDue sends every planned probe whose second has come, in planned order, while the period lasts and the
// per-source limit of its route class allows. Each observation is journaled as soon as it is made.
func qualityExecuteDue(s *Service, ctx context.Context, member *Member, key string, plan *qualityPlan, now time.Time) error {
	end, err := wire.ParseTime(plan.PeriodEnd)
	if err != nil || !now.Before(end) {
		return err
	}
	for i := range plan.Probes {
		probe := &plan.Probes[i]
		if probe.Observation != nil || probe.StartedAt > wire.FormatTime(now) {
			continue
		}
		limit := qualityListingPerSourcePerMinute
		if probe.Kind == string(quality.KindRetrieval) {
			limit = qualityDirectPerSourcePerMinute
		}
		admitted, err := qualityAdmit(s, plan.ProviderURL, quality.Kind(probe.Kind), limit, now)
		if err != nil {
			return err
		}
		if !admitted {
			continue
		}
		observation := qualitySend(s, ctx, plan, *probe)
		probe.Observation = &observation
		if err := member.DB().Update(func(tx store.Tx) error { return qualityPlans.Put(tx, key, *plan) }); err != nil {
			return err
		}
	}
	return nil
}

// qualitySend makes one probe under one deadline with no retry. started_at is the planned second and the duration
// comes from the monotonic clock.
func qualitySend(s *Service, ctx context.Context, plan *qualityPlan, probe qualityPlannedProbe) wire.QualityObservation {
	base := s.HTTPClient().Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &qualityStatusTransport{base: base}
	kind := quality.Kind(probe.Kind)
	observation := wire.QualityObservation{Kind: probe.Kind, MessageID: probe.MessageID, StartedAt: probe.StartedAt, WithinLimits: true}
	client, err := sdk.New(plan.ProviderURL, sdk.Options{ServiceID: plan.ProviderID, AllowLoopbackHTTP: true, HTTPClient: &http.Client{Transport: transport}, Timeout: qualityProbeTimeout, Now: s.Now})
	if err != nil {
		observation.Outcome = string(quality.OutcomeUnknown)
		return observation
	}
	ctx, cancel := context.WithTimeout(ctx, qualityProbeTimeout)
	defer cancel()
	started := time.Now()
	if kind == quality.KindAvailability {
		err = qualityList(ctx, client)
	} else {
		err = qualityRetrieve(ctx, client, s.Now(), probe)
	}
	observation.DurationMs = time.Since(started).Milliseconds()
	var status *int64
	if transport.status != 0 && sdk.Classify(err) != sdk.SignalTimeout {
		code := int64(transport.status)
		status = &code
	}
	observation.HTTPStatus = status
	legal := sdk.Classify(err) == sdk.SignalLegalRestricted
	observation.Outcome = string(quality.Classify(kind, status, err == nil, legal, kind == quality.KindRetrieval))
	return observation
}

// qualityList requests one listing page and validates it as a DiscoveryPage.
func qualityList(ctx context.Context, client *sdk.Client) error {
	pager, err := sdk.NewPager(client, sdk.MessagesRoute(), sdk.ListQuery{Limit: 1})
	if err != nil {
		return err
	}
	_, err = pager.Next(ctx)
	return err
}

// qualityRetrieve fetches the sampled message and checks its ID, intent ID, signatures and the settled receipt that put it in the frame.
func qualityRetrieve(ctx context.Context, client *sdk.Client, now time.Time, probe qualityPlannedProbe) error {
	fetched, err := client.GetMessage(ctx, *probe.MessageID)
	if err != nil {
		return err
	}
	if err := message.Verify(fetched.Message, message.VerifyOptions{Now: now, Delegation: delegation.CheckIntentDelegation}); err != nil {
		return err
	}
	hash, err := settlement.ReceiptHash(fetched.Message.SubmissionReceipt)
	if err != nil {
		return err
	}
	if hash != probe.ReceiptHash {
		return errors.New("monitor: retrieved receipt differs from the settled receipt in the frame")
	}
	return nil
}
