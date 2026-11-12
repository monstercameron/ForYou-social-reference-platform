package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/wire"
)

// ErrSnapshotLost means the caller must restart the original interval from its beginning.
var ErrSnapshotLost = errors.New("sdk: snapshot lost; restart the original interval")

// ErrBudget means the snapshot scan reached its configured page limit.
var ErrBudget = errors.New("sdk: snapshot page budget exhausted")

// Page is one validated snapshot page. Done is true only when next_cursor is null.
type Page struct {
	// Messages are the page's exact nested JSON records in validated order.
	Messages []Fetched
	// Done indicates the response carried a null next_cursor.
	Done bool
}

// Pager scans a bounded provider-local snapshot and validates its ordering and predicates.
type Pager struct {
	client             *Client
	route              Route
	query              ListQuery
	cursor             string
	generated, expires time.Time
	initialized        bool
	seenCursor         map[string]bool
	seenIDs            map[string]bool
	last               *discovery.Record
	pages              int
	// MaxPages bounds the scan and defaults to twenty.
	MaxPages int
	done     bool
	failed   bool
}

// NewPager validates a listing request and creates a snapshot scanner.
func NewPager(c *Client, r Route, q ListQuery) (*Pager, error) {
	if c == nil {
		return nil, errors.New("sdk: nil client")
	}
	if _, _, err := r.query(q, ""); err != nil {
		return nil, err
	}
	return &Pager{client: c, route: r, query: q, seenCursor: map[string]bool{}, seenIDs: map[string]bool{}, MaxPages: 20}, nil
}

// Next returns the next page, stopping on any snapshot, ordering, uniqueness, or predicate violation.
// A lost snapshot requires restarting the original interval; never resume below the last timestamp.
func (p *Pager) Next(ctx context.Context) (Page, error) {
	if p.failed {
		return Page{}, rdProtocol("pager stopped after invalid response")
	}
	if p.done {
		return Page{Done: true}, nil
	}
	if p.pages >= p.MaxPages {
		return Page{}, ErrBudget
	}
	query, parsedQuery, err := p.route.query(p.query, p.cursor)
	if err != nil {
		return Page{}, err
	}
	var decoded wire.DiscoveryPage
	raw, err := p.client.getJSON(ctx, p.route.path(), query, rdPageLimit, &decoded)
	if err != nil {
		if rdStatus(err, "invalid_cursor") {
			p.failed = true
			return Page{}, ErrSnapshotLost
		}
		if Classify(err) == SignalProtocol {
			p.failed = true
		}
		return Page{}, err
	}
	responseAt := p.client.now()
	p.pages++
	if len(decoded.Messages) > parsedQuery.Limit {
		return Page{}, p.rdFail("page exceeds requested message limit")
	}
	generated, err := rdParseTimestamp(decoded.GeneratedAt)
	if err != nil {
		return Page{}, p.rdFail("invalid generated_at")
	}
	expires, err := rdParseTimestamp(decoded.ExpiresAt)
	if err != nil {
		return Page{}, p.rdFail("invalid expires_at")
	}
	if generated.After(p.client.now().Add(60 * time.Second)) {
		return Page{}, p.rdFail("snapshot generated_at is too far ahead")
	}
	duration := expires.Sub(generated)
	if duration < 15*time.Minute || duration > 60*time.Minute {
		return Page{}, p.rdFail("snapshot lifetime outside allowed range")
	}
	if !responseAt.Before(expires) {
		p.failed = true
		return Page{}, ErrSnapshotLost
	}
	if p.initialized && (!generated.Equal(p.generated) || !expires.Equal(p.expires)) {
		return Page{}, p.rdFail("snapshot changed during pagination")
	}
	if decoded.NextCursor != nil {
		if p.seenCursor[*decoded.NextCursor] {
			return Page{}, p.rdFail("repeated snapshot cursor")
		}
		p.seenCursor[*decoded.NextCursor] = true
	}
	var exact struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &exact); err != nil || len(exact.Messages) != len(decoded.Messages) {
		return Page{}, p.rdFail("invalid message page encoding")
	}
	page := Page{Messages: make([]Fetched, 0, len(decoded.Messages)), Done: decoded.NextCursor == nil}
	for i, message := range decoded.Messages {
		if int64(len(exact.Messages[i])) > rdObjectLimit {
			return Page{}, p.rdFail("nested message exceeds limit")
		}
		record, parseErr := discovery.ParseRecord(exact.Messages[i], 0)
		if parseErr != nil || record.MessageID != message.MessageID {
			return Page{}, p.rdFail("invalid nested message record")
		}
		if p.seenIDs[message.MessageID] {
			return Page{}, p.rdFail("duplicate message ID across snapshot")
		}
		if p.last != nil && !discovery.Before(*p.last, record) {
			return Page{}, p.rdFail("snapshot messages out of order")
		}
		if !parsedQuery.Match(record) {
			return Page{}, p.rdFail("message does not match listing route")
		}
		p.seenIDs[message.MessageID] = true
		copyRaw := append([]byte(nil), exact.Messages[i]...)
		page.Messages = append(page.Messages, Fetched{Raw: copyRaw, Message: message, ID: message.MessageID, Endpoint: p.client.base.String(), ServiceID: p.client.serviceID})
		p.last = &record
	}
	p.generated, p.expires, p.initialized = generated, expires, true
	if decoded.NextCursor != nil {
		p.cursor = *decoded.NextCursor
	} else {
		p.done = true
	}
	return page, nil
}

func (p *Pager) rdFail(message string) error {
	p.failed = true
	return rdProtocol(message)
}
