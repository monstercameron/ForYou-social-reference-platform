package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/quality"
	"foryou.local/foryou/internal/wire"
)

// Witnesses retrieves one committee monitor's witness page.
func (c *Client) Witnesses(ctx context.Context, providerID, network string, afterSequence uint64) (wire.MonitorWitnessesPage, []byte, error) {
	if err := rdValidateProviderNetwork(providerID, network, afterSequence); err != nil {
		return wire.MonitorWitnessesPage{}, nil, err
	}
	q := url.Values{"provider_id": {providerID}, "network": {network}, "after_sequence": {strconv.FormatUint(afterSequence, 10)}}
	var out wire.MonitorWitnessesPage
	raw, err := c.getJSON(ctx, "/v1/witnesses", q, rdObjectLimit, &out)
	return out, raw, err
}

// Equivocations retrieves a monitor's proofs for one provider.
func (c *Client) Equivocations(ctx context.Context, providerID string) (wire.EquivocationsPage, []byte, error) {
	if _, err := rdParseDID(providerID); err != nil {
		return wire.EquivocationsPage{}, nil, err
	}
	q := url.Values{"provider_id": {providerID}}
	var out wire.EquivocationsPage
	raw, err := c.getJSON(ctx, "/v1/equivocations", q, rdObjectLimit, &out)
	return out, raw, err
}

// QualityReports retrieves one monitor's quality reports for a provider and quarter.
func (c *Client) QualityReports(ctx context.Context, providerID, quarterEnd string) (wire.QualityReportsPage, []byte, error) {
	if err := rdValidateQualityQuery(providerID, quarterEnd); err != nil {
		return wire.QualityReportsPage{}, nil, err
	}
	q := url.Values{"provider_id": {providerID}, "quarter_end": {quarterEnd}}
	var out wire.QualityReportsPage
	raw, err := c.getJSON(ctx, "/v1/quality-reports", q, rdObjectLimit, &out)
	return out, raw, err
}

// qualityEvidenceLimit bounds an observation set response: the 4 MiB the quality rules allow.
const qualityEvidenceLimit = 4 << 20

// QualityEvidence fetches the methodology and observation set a monitor serves for
// one of its reports and checks them against the report: both digests, the report's
// provider, monitor and period, and every count and percentile recomputed from the
// observations. An error means the report must not be relied on.
func (c *Client) QualityEvidence(ctx context.Context, report wire.QualityReport) (wire.QualityObservationSet, []byte, error) {
	base := "/v1/quality-reports/" + rdEscape(report.ReportID)
	methodology, err := c.send(ctx, http.MethodGet, base+"/methodology", nil, nil, http.Header{"Accept": {"text/plain"}}, 1<<20)
	if err != nil {
		return wire.QualityObservationSet{}, nil, err
	}
	observations, err := c.send(ctx, http.MethodGet, base+"/observations", nil, nil, nil, qualityEvidenceLimit)
	if err != nil {
		return wire.QualityObservationSet{}, nil, err
	}
	if methodology.status != http.StatusOK || observations.status != http.StatusOK {
		return wire.QualityObservationSet{}, nil, rdProtocol("unexpected quality evidence status")
	}
	served := sha256.Sum256(observations.body)
	if hex.EncodeToString(served[:]) != report.ObservationsDigest {
		return wire.QualityObservationSet{}, nil, rdProtocol("observation set does not match the report's digest")
	}
	var set wire.QualityObservationSet
	if err := wire.DecodeLimits(observations.body, canon.Limits{MaxBytes: qualityEvidenceLimit, MaxDepth: canon.MessageLimits.MaxDepth}, "QualityObservationSet", &set); err != nil {
		return wire.QualityObservationSet{}, nil, rdProtocol("invalid QualityObservationSet response")
	}
	if err := quality.Verify(report, set, methodology.body); err != nil {
		return wire.QualityObservationSet{}, nil, rdProtocol("quality evidence does not verify: " + err.Error())
	}
	return set, methodology.body, nil
}
