package sdk

import (
	"context"
	"net/url"
	"strconv"

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
