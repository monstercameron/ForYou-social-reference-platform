package sdk

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// Fetched holds the exact response bytes and decoded transport message; callers must verify it before trust.
type Fetched struct {
	// Raw is the exact UTF-8 JSON response or nested message bytes.
	Raw []byte
	// Message is the strictly decoded message; it is not verified by transport.
	Message wire.TransportMessage
	// ID is the requested message ID.
	ID string
	// Endpoint is the service base URL.
	Endpoint string
	// ServiceID is the pinned operator identity of the service.
	ServiceID string
}

// GetMessage retrieves one exact retained message by its canonical message ID.
func (c *Client) GetMessage(ctx context.Context, id string) (Fetched, error) {
	if !rdValidMessageID(id) {
		return Fetched{}, discovery.ErrInvalidQuery
	}
	var message wire.TransportMessage
	raw, err := c.getJSON(ctx, "/v1/messages/"+rdEscape(id), nil, rdObjectLimit, &message)
	if err != nil {
		return Fetched{}, err
	}
	if message.MessageID != id {
		return Fetched{}, rdProtocol("response message ID differs from request")
	}
	return Fetched{Raw: raw, Message: message, ID: id, Endpoint: c.base.String(), ServiceID: c.serviceID}, nil
}

// Retention returns the provider's retention declarations and their raw response bytes.
func (c *Client) Retention(ctx context.Context) (wire.RetentionDeclarationsPage, []byte, error) {
	var out wire.RetentionDeclarationsPage
	raw, err := c.getJSON(ctx, "/v1/retention", nil, rdObjectLimit, &out)
	return out, raw, err
}

// Successions returns key succession records; a provider-local 404 is reported in signal with an empty result.
func (c *Client) Successions(ctx context.Context, did string) ([]wire.KeySuccession, []byte, Signal, error) {
	if _, err := keys.ParseDID(did); err != nil {
		return nil, nil, SignalOther, discovery.ErrInvalidQuery
	}
	res, err := c.send(ctx, http.MethodGet, "/v1/successions/"+rdEscape(did), nil, nil, nil, rdObjectLimit)
	if err == nil && res.status != http.StatusOK {
		err = rdProtocol("unexpected success status")
	}
	if rdStatus(err, "not_found") {
		return []wire.KeySuccession{}, nil, SignalNotFound, nil
	}
	if err != nil {
		return nil, nil, Classify(err), err
	}
	items, err := rdDecodeSuccessions(res.body)
	if err != nil {
		return nil, nil, SignalProtocol, err
	}
	for _, item := range items {
		if item.OldID != did {
			return nil, nil, SignalProtocol, rdProtocol("succession old_id differs from request")
		}
	}
	return items, append([]byte(nil), res.body...), SignalOther, nil
}

func rdDecodeSuccessions(body []byte) ([]wire.KeySuccession, error) {
	value, err := canon.Parse(body, canon.MessageLimits)
	if err != nil {
		return nil, rdProtocol("invalid successions response")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 1 {
		return nil, rdProtocol("invalid successions response")
	}
	encoded, ok := object["successions"].([]any)
	if !ok {
		return nil, rdProtocol("invalid successions response")
	}
	var envelope struct {
		Successions []json.RawMessage `json:"successions"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Successions == nil || len(envelope.Successions) != len(encoded) {
		return nil, rdProtocol("invalid successions response")
	}
	items := make([]wire.KeySuccession, 0, len(envelope.Successions))
	for _, raw := range envelope.Successions {
		var item wire.KeySuccession
		if err := wire.Decode(raw, "KeySuccession", &item); err != nil {
			return nil, rdProtocol("invalid succession object")
		}
		items = append(items, item)
	}
	return items, nil
}

// OperatorManifest returns the signed operator disclosure and raw response bytes.
func (c *Client) OperatorManifest(ctx context.Context) (wire.OperatorManifest, []byte, error) {
	var out wire.OperatorManifest
	raw, err := c.getJSON(ctx, "/.well-known/foryou-operator.json", nil, rdObjectLimit, &out)
	return out, raw, err
}

// Settlement retrieves a settlement proof for a canonical receipt ID.
func (c *Client) Settlement(ctx context.Context, receiptID string) (wire.SettlementProof, []byte, error) {
	if !rdValidReceiptID(receiptID) {
		return wire.SettlementProof{}, nil, discovery.ErrInvalidQuery
	}
	var out wire.SettlementProof
	raw, err := c.getJSON(ctx, "/v1/settlements/"+receiptID, nil, rdObjectLimit, &out)
	return out, raw, err
}

// SettlementClaims retrieves at most 100 claims after a provider-local sequence.
func (c *Client) SettlementClaims(ctx context.Context, network string, afterSequence uint64) (wire.SettlementClaimsPage, []byte, error) {
	if !rdNetwork(network) || afterSequence > discovery.MaxAfter {
		return wire.SettlementClaimsPage{}, nil, discovery.ErrInvalidQuery
	}
	q := url.Values{"network": {network}, "after_sequence": {strconv.FormatUint(afterSequence, 10)}}
	var out wire.SettlementClaimsPage
	raw, err := c.getJSON(ctx, "/v1/settlement-claims", q, rdObjectLimit, &out)
	if err == nil {
		if len(out.Claims) > 100 {
			return out, nil, rdProtocol("too many claims")
		}
		prev := afterSequence
		for _, claim := range out.Claims {
			seq := uint64(claim.Sequence)
			if seq <= prev {
				return out, nil, rdProtocol("claims are not strictly increasing")
			}
			prev = seq
		}
	}
	return out, raw, err
}

// ClaimLeaves retrieves one bounded page of leaves and checks its echoed pagination fields.
func (c *Client) ClaimLeaves(ctx context.Context, claimID string, offset uint64) (wire.SettlementLeavesPage, []byte, error) {
	if !rdValidClaimID(claimID) || offset > discovery.MaxAfter {
		return wire.SettlementLeavesPage{}, nil, discovery.ErrInvalidQuery
	}
	q := url.Values{"offset": {strconv.FormatUint(offset, 10)}}
	var out wire.SettlementLeavesPage
	raw, err := c.getJSON(ctx, "/v1/settlement-claims/"+rdEscape(claimID)+"/leaves", q, rdObjectLimit, &out)
	if err == nil && (out.ClaimID != claimID || uint64(out.Offset) != offset || len(out.Leaves) > 2000) {
		return out, nil, rdProtocol("claim leaves page does not match request")
	}
	return out, raw, err
}

// ClaimWitnesses retrieves the witnesses the provider holds for a claim.
func (c *Client) ClaimWitnesses(ctx context.Context, claimID string) (wire.SettlementWitnessesPage, []byte, error) {
	if !rdValidClaimID(claimID) {
		return wire.SettlementWitnessesPage{}, nil, discovery.ErrInvalidQuery
	}
	var out wire.SettlementWitnessesPage
	raw, err := c.getJSON(ctx, "/v1/settlement-claims/"+rdEscape(claimID)+"/witnesses", nil, rdObjectLimit, &out)
	return out, raw, err
}

// ListQuery contains the date and page-size filters shared by public listing routes.
type ListQuery struct {
	// Date is a mutually exclusive UTC date filter.
	Date, From, Until string
	// From is inclusive and Until exclusive; each uses canonical UTC seconds.
	// Limit is zero for the protocol default or a value from one through one hundred.
	Limit int
}

// Route is a locally validated listing endpoint and optional exact target.
type Route struct {
	kind   discovery.Route
	target string
	author string
	valid  bool
}

// MessagesRoute builds the unfiltered message listing route.
func MessagesRoute() Route { return Route{kind: discovery.Messages, valid: true} }

// AuthorRoute builds the route for one canonical author DID.
func AuthorRoute(authorID string) (Route, error) {
	if _, err := keys.ParseDID(authorID); err != nil {
		return Route{}, fmt.Errorf("sdk: invalid author DID")
	}
	return Route{kind: discovery.AuthorMessages, author: authorID, valid: true}, nil
}

// RepliesRoute builds the exact reply lookup route.
func RepliesRoute(messageID string) (Route, error) {
	if !rdValidMessageID(messageID) {
		return Route{}, fmt.Errorf("sdk: invalid message ID")
	}
	return Route{kind: discovery.Replies, target: messageID, valid: true}, nil
}

// ReferencesRoute builds the exact declared-reference lookup route.
func ReferencesRoute(target string) (Route, error) {
	if !rdValidMessageID(target) {
		if _, err := keys.ParseDID(target); err != nil {
			return Route{}, fmt.Errorf("sdk: invalid reference target")
		}
	}
	return Route{kind: discovery.References, target: target, valid: true}, nil
}

// VersionsRoute builds the immediate-successor lookup route.
func VersionsRoute(messageID string) (Route, error) {
	if !rdValidMessageID(messageID) {
		return Route{}, fmt.Errorf("sdk: invalid message ID")
	}
	return Route{kind: discovery.Versions, target: messageID, valid: true}, nil
}

func (rd Route) path() string {
	switch rd.kind {
	case discovery.Messages:
		return "/v1/messages"
	case discovery.AuthorMessages:
		return "/v1/author-messages"
	case discovery.Replies:
		return "/v1/replies/" + rdEscape(rd.target)
	case discovery.References:
		return "/v1/references/" + rdEscape(rd.target)
	case discovery.Versions:
		return "/v1/versions/" + rdEscape(rd.target)
	default:
		return ""
	}
}
func (rd Route) query(q ListQuery, cursor string) (url.Values, discovery.Query, error) {
	if !rd.valid || q.Limit < 0 {
		return nil, discovery.Query{}, discovery.ErrInvalidQuery
	}
	values := url.Values{}
	if q.Date != "" {
		values.Set("date", q.Date)
	}
	if q.From != "" {
		values.Set("from", q.From)
	}
	if q.Until != "" {
		values.Set("until", q.Until)
	}
	if q.Limit != 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	if rd.kind == discovery.AuthorMessages {
		values.Set("author_id", rd.author)
	}
	parsed, _, err := discovery.ParseListQuery(rd.kind, rd.target, rdEncodeQuery(values))
	if err != nil {
		return nil, discovery.Query{}, err
	}
	return values, parsed, nil
}

// ChangesResult holds one incremental-sync page and its exact response bytes.
type ChangesResult struct {
	// Page is the strictly decoded incremental-sync response.
	Page wire.ChangesPage
	// Raw is the exact UTF-8 JSON response body.
	Raw []byte
}

// Changes retrieves and validates one incremental-sync page. Sequences are provider-local and consuming a stream is not a completeness proof.
func (c *Client) Changes(ctx context.Context, after uint64, limit int) (ChangesResult, error) {
	if after > discovery.MaxAfter || limit < 1 || limit > 100 {
		return ChangesResult{}, discovery.ErrInvalidQuery
	}
	q := url.Values{"after": {strconv.FormatUint(after, 10)}, "limit": {strconv.Itoa(limit)}}
	var out wire.ChangesPage
	raw, err := c.getJSON(ctx, "/v1/changes", q, rdPageLimit, &out)
	if err != nil {
		return ChangesResult{Page: out}, err
	}
	if out.ProviderID != c.serviceID || len(out.Items) > limit || uint64(out.NextAfter) < after {
		return ChangesResult{Page: out}, rdProtocol("invalid changes page bounds")
	}
	var exact struct {
		Items []struct {
			Message json.RawMessage `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &exact); err != nil || len(exact.Items) != len(out.Items) {
		return ChangesResult{Page: out}, rdProtocol("invalid changes page encoding")
	}
	for _, item := range exact.Items {
		if int64(len(item.Message)) > rdObjectLimit {
			return ChangesResult{Page: out}, rdProtocol("changes message exceeds limit")
		}
	}
	previous := after
	greatest := after
	for _, item := range out.Items {
		seq := uint64(item.Sequence)
		if seq <= previous || seq <= after {
			return ChangesResult{Page: out}, rdProtocol("changes sequences are not increasing")
		}
		previous = seq
		greatest = seq
	}
	if len(out.Items) > 0 && uint64(out.NextAfter) < greatest {
		return ChangesResult{Page: out}, rdProtocol("next_after does not cover the final item")
	}
	return ChangesResult{Page: out, Raw: raw}, nil
}

func rdValidMessageID(id string) bool {
	if len(id) != 68 || !strings.HasPrefix(id, "fy1:") {
		return false
	}
	for i := 4; i < len(id); i++ {
		if !(id[i] >= '0' && id[i] <= '9' || id[i] >= 'a' && id[i] <= 'f') {
			return false
		}
	}
	return true
}
func rdValidReceiptID(id string) bool {
	if len(id) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(id)
	return err == nil && len(decoded) == 32
}
func rdValidClaimID(id string) bool {
	if len(id) != 70 || !strings.HasPrefix(id, "fysc2:") {
		return false
	}
	for i := len("fysc2:"); i < len(id); i++ {
		if !(id[i] >= '0' && id[i] <= '9' || id[i] >= 'a' && id[i] <= 'f') {
			return false
		}
	}
	return true
}
func rdNetwork(n string) bool { return n == "bitcoin-mainnet" || n == "ethereum-mainnet" }
