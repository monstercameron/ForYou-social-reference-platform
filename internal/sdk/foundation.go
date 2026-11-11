package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// Genesis retrieves the Foundation genesis manifest and raw bytes.
func (c *Client) Genesis(ctx context.Context) (wire.GenesisManifest, []byte, error) {
	var out wire.GenesisManifest
	raw, err := c.getJSON(ctx, "/v1/foundation/genesis", nil, rdObjectLimit, &out)
	return out, raw, err
}

// Certificates retrieves Foundation certificates and raw bytes.
func (c *Client) Certificates(ctx context.Context) (wire.FoundationCertificatesPage, []byte, error) {
	var out wire.FoundationCertificatesPage
	raw, err := c.getJSON(ctx, "/v1/foundation/certificates", nil, rdObjectLimit, &out)
	return out, raw, err
}

// FeeSchedules retrieves Foundation fee schedules and raw bytes.
func (c *Client) FeeSchedules(ctx context.Context) (wire.FeeSchedulesPage, []byte, error) {
	var out wire.FeeSchedulesPage
	raw, err := c.getJSON(ctx, "/v1/foundation/fee-schedules", nil, rdObjectLimit, &out)
	return out, raw, err
}

// Distribution retrieves Foundation distribution publications and raw bytes.
func (c *Client) Distribution(ctx context.Context) (wire.DistributionPage, []byte, error) {
	var out wire.DistributionPage
	raw, err := c.getJSON(ctx, "/v1/foundation/distribution", nil, rdObjectLimit, &out)
	return out, raw, err
}

// VaultStatus retrieves Foundation vault status publications and raw bytes.
func (c *Client) VaultStatus(ctx context.Context) (wire.VaultStatusPage, []byte, error) {
	var out wire.VaultStatusPage
	raw, err := c.getJSON(ctx, "/v1/foundation/vault-status", nil, rdObjectLimit, &out)
	return out, raw, err
}

// FoundationQualityReports retrieves copied monitor reports for a provider and quarter.
func (c *Client) FoundationQualityReports(ctx context.Context, providerID, quarterEnd string) (wire.QualityReportsPage, []byte, error) {
	if err := rdValidateQualityQuery(providerID, quarterEnd); err != nil {
		return wire.QualityReportsPage{}, nil, err
	}
	q := url.Values{"provider_id": {providerID}, "quarter_end": {quarterEnd}}
	var out wire.QualityReportsPage
	raw, err := c.getJSON(ctx, "/v1/foundation/quality-reports", q, rdObjectLimit, &out)
	return out, raw, err
}

// Log retrieves at most 1,000 Foundation log entries after an optional index.
func (c *Client) Log(ctx context.Context, after *uint64) (wire.FoundationLogPage, []byte, error) {
	if after != nil && *after > 1<<53-1 {
		return wire.FoundationLogPage{}, nil, discovery.ErrInvalidQuery
	}
	q := url.Values{}
	if after != nil {
		q.Set("after", strconv.FormatUint(*after, 10))
	}
	var out wire.FoundationLogPage
	raw, err := c.getJSON(ctx, "/v1/foundation/log", q, rdObjectLimit, &out)
	if err == nil {
		if len(out.Entries) > 1000 {
			return out, nil, rdProtocol("too many log entries")
		}
		prev := uint64(0)
		if after != nil {
			prev = *after
		}
		first := after == nil
		for _, entry := range out.Entries {
			index := uint64(entry.Index)
			if !first && index <= prev {
				return out, nil, rdProtocol("log entries not increasing")
			}
			prev = index
			first = false
		}
	}
	return out, raw, err
}

// LogHeads retrieves at most 1,000 signed log heads after a size.
func (c *Client) LogHeads(ctx context.Context, afterSize uint64) (wire.FoundationLogHeadsPage, []byte, error) {
	if afterSize > 1<<53-1 {
		return wire.FoundationLogHeadsPage{}, nil, discovery.ErrInvalidQuery
	}
	q := url.Values{"after_size": {strconv.FormatUint(afterSize, 10)}}
	var out wire.FoundationLogHeadsPage
	raw, err := c.getJSON(ctx, "/v1/foundation/log/heads", q, rdObjectLimit, &out)
	if err == nil && len(out.Heads) > 1000 {
		return out, nil, rdProtocol("too many log heads")
	}
	return out, raw, err
}

// LogProof retrieves an inclusion proof for a canonical lowercase object hash.
func (c *Client) LogProof(ctx context.Context, objectHash string, size uint64) (wire.FoundationLogProof, []byte, error) {
	if !rdHash(objectHash) || size > 1<<53-1 {
		return wire.FoundationLogProof{}, nil, discovery.ErrInvalidQuery
	}
	q := url.Values{"size": {strconv.FormatUint(size, 10)}}
	var out wire.FoundationLogProof
	raw, err := c.getJSON(ctx, "/v1/foundation/log/proof/"+objectHash, q, rdObjectLimit, &out)
	return out, raw, err
}

// LogObject retrieves the JCS object bytes committed by an object hash.
func (c *Client) LogObject(ctx context.Context, objectHash string) (json.RawMessage, []byte, error) {
	if !rdHash(objectHash) {
		return nil, nil, discovery.ErrInvalidQuery
	}
	var out json.RawMessage
	raw, err := c.getJSON(ctx, "/v1/foundation/log/objects/"+objectHash, nil, rdObjectLimit, &out)
	if err != nil {
		return nil, nil, err
	}
	canonical, err := canon.Canonicalize(out, canon.MessageLimits)
	if err != nil {
		return nil, nil, rdProtocol("invalid logged object")
	}
	if err := rdDecodeLoggedObject(out); err != nil {
		return nil, nil, rdProtocol("invalid logged object schema")
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != objectHash {
		return nil, nil, rdProtocol("logged object hash mismatch")
	}
	return out, raw, nil
}

func rdDecodeLoggedObject(raw json.RawMessage) error {
	value, err := canon.Parse(raw, canon.MessageLimits)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("logged object must be an object")
	}
	if _, ok := object["message_id"]; ok {
		var receipt wire.VaultReceipt
		return wire.Decode(raw, "VaultReceipt", &receipt)
	}
	if _, ok := object["statement_id"]; ok {
		var statement wire.DistributionStatement
		return wire.Decode(raw, "DistributionStatement", &statement)
	}
	if _, ok := object["record_count"]; ok {
		var status wire.VaultStatus
		return wire.Decode(raw, "VaultStatus", &status)
	}
	return fmt.Errorf("unknown logged object kind")
}

// VaultReceipt retrieves the public receipt for one vault message.
func (c *Client) VaultReceipt(ctx context.Context, messageID string) (wire.VaultReceipt, []byte, error) {
	if !rdValidMessageID(messageID) {
		return wire.VaultReceipt{}, nil, discovery.ErrInvalidQuery
	}
	var out wire.VaultReceipt
	raw, err := c.getJSON(ctx, "/v1/vault/receipts/"+rdEscape(messageID), nil, rdObjectLimit, &out)
	if err == nil && out.MessageID != messageID {
		return out, nil, rdProtocol("vault receipt message ID differs from request")
	}
	return out, raw, err
}

// DeliverToVault submits a message and settlement proof; status distinguishes newly held from already held.
func (c *Client) DeliverToVault(ctx context.Context, delivery wire.VaultDelivery) (wire.VaultReceipt, []byte, int, error) {
	body, err := wire.Encode("VaultDelivery", delivery)
	if err != nil {
		return wire.VaultReceipt{}, nil, 0, fmt.Errorf("sdk: invalid vault delivery: %w", err)
	}
	messageID := rdExtractDeliveryID(delivery.Message)
	if !rdValidMessageID(messageID) {
		return wire.VaultReceipt{}, nil, 0, discovery.ErrInvalidQuery
	}
	res, err := c.send(ctx, http.MethodPut, "/v1/vault/messages/"+rdEscape(messageID), nil, body, nil, rdObjectLimit)
	if err != nil {
		var publicError *Error
		if errors.As(err, &publicError) {
			return wire.VaultReceipt{}, nil, publicError.HTTPStatus, err
		}
		return wire.VaultReceipt{}, nil, 0, err
	}
	if res.status != http.StatusCreated && res.status != http.StatusOK {
		return wire.VaultReceipt{}, nil, res.status, rdProtocol("unexpected vault delivery status")
	}
	var receipt wire.VaultReceipt
	if err := wire.Decode(res.body, "VaultReceipt", &receipt); err != nil {
		return wire.VaultReceipt{}, nil, res.status, rdProtocol("invalid vault receipt")
	}
	if receipt.MessageID != messageID {
		return receipt, nil, res.status, rdProtocol("vault receipt message ID differs from delivery")
	}
	return receipt, res.body, res.status, nil
}

// rdValidateQualityQuery validates shared provider and canonical UTC quarter-end filters.
func rdValidateQualityQuery(providerID, quarterEnd string) error {
	if _, err := rdParseDID(providerID); err != nil {
		return err
	}
	if _, err := rdParseTimestamp(quarterEnd); err != nil {
		return discovery.ErrInvalidQuery
	}
	return nil
}
func rdValidateProviderNetwork(providerID, network string, after uint64) error {
	if _, err := rdParseDID(providerID); err != nil {
		return err
	}
	if !rdNetwork(network) || after > 1<<53-1 {
		return discovery.ErrInvalidQuery
	}
	return nil
}
func rdParseDID(did string) ([]byte, error) {
	key, err := keys.ParseDID(did)
	if err != nil {
		return nil, discovery.ErrInvalidQuery
	}
	return key, nil
}
func rdHash(v string) bool {
	if len(v) != 64 || strings.ToLower(v) != v {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
func rdExtractDeliveryID(raw json.RawMessage) string {
	var envelope struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return envelope.MessageID
}
