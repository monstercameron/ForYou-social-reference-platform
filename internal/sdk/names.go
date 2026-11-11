package sdk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
)

// NamePublishResult reports whether a holder accepted or already held a name record.
type NamePublishResult int

const (
	// NamePublished indicates that the holder committed the record.
	NamePublished NamePublishResult = iota + 1
	// NameAlreadyHeld indicates that the holder already had the identical current record.
	NameAlreadyHeld
)

// PublishName publishes a verified, self-authenticating record. If IsAmbiguous reports an ambiguous outcome, the caller can retry by resending the same record and bytes; an identical record is safe to retry because it is self-authenticating.
func (c *Client) PublishName(ctx context.Context, rec names.NameRecord) (NamePublishResult, error) {
	if err := names.Verify(rec, c.now()); err != nil {
		return 0, fmt.Errorf("sdk: name record: %w", err)
	}
	body, err := names.Encode(rec)
	if err != nil {
		return 0, fmt.Errorf("sdk: encode name record: %w", err)
	}
	res, err := c.send(ctx, http.MethodPost, "/v1/names", nil, body, nil, names.MaxRecordBytes)
	if err != nil {
		return 0, err
	}
	if res.status != http.StatusCreated && res.status != http.StatusOK {
		return 0, rdProtocol("unexpected name publish status")
	}
	if !bytes.Equal(res.body, body) {
		return 0, rdProtocol("published name response differs from request")
	}
	if res.status == http.StatusCreated {
		return NamePublished, nil
	}
	return NameAlreadyHeld, nil
}

// Name reads and verifies the holder's current record for authorID. A holder-local 404 is returned as an Error with SignalNotFound.
func (c *Client) Name(ctx context.Context, authorID string) (names.NameRecord, error) {
	if _, err := keys.ParseDID(authorID); err != nil {
		return names.NameRecord{}, discovery.ErrInvalidQuery
	}
	res, err := c.send(ctx, http.MethodGet, "/v1/names/"+rdEscape(authorID), nil, nil, nil, names.MaxRecordBytes)
	if err != nil {
		return names.NameRecord{}, err
	}
	if res.status != http.StatusOK {
		return names.NameRecord{}, rdProtocol("unexpected name read status")
	}
	record, err := names.Parse(res.body)
	if err != nil || record.AuthorID != authorID || names.Verify(record, c.now()) != nil {
		return names.NameRecord{}, rdProtocol("invalid name record response")
	}
	return record, nil
}

// NameLookup scans advisory name candidates from one holder. Results are not a uniqueness claim, and the lookup reveals the reader's interest to that holder.
type NameLookup struct {
	client      *Client
	query       string
	cursor      string
	seenCursor  map[string]bool
	seenAuthors map[string]bool
	lastAuthor  string
	pages       int
	done        bool
	failed      bool
	// MaxPages bounds this scan and defaults to five.
	MaxPages int
}

// NameLookupPage is one verified page of advisory name candidates.
type NameLookupPage struct {
	Records []names.NameRecord
	Done    bool
}

// NewNameLookup validates and normalizes a query before constructing a holder-local scan.
func NewNameLookup(c *Client, query string) (*NameLookup, error) {
	if c == nil {
		return nil, errors.New("sdk: nil client")
	}
	normalized, err := names.CheckQuery(query)
	if err != nil {
		return nil, err
	}
	return &NameLookup{client: c, query: normalized, seenCursor: map[string]bool{}, seenAuthors: map[string]bool{}, MaxPages: 5}, nil
}

// Next returns the next verified lookup page and stops after any invalid response.
func (l *NameLookup) Next(ctx context.Context) (NameLookupPage, error) {
	if l.failed {
		return NameLookupPage{}, rdProtocol("name lookup stopped after invalid response")
	}
	if l.done {
		return NameLookupPage{Done: true}, nil
	}
	if l.pages >= l.MaxPages {
		return NameLookupPage{}, ErrBudget
	}
	query := url.Values{"display_name": {l.query}}
	if l.cursor != "" {
		query.Set("cursor", l.cursor)
	}
	res, err := l.client.send(ctx, http.MethodGet, "/v1/names", query, nil, nil, names.MaxPageBytes)
	if err != nil {
		if rdStatus(err, "invalid_cursor") {
			l.failed = true
			return NameLookupPage{}, ErrSnapshotLost
		}
		if Classify(err) == SignalProtocol {
			l.failed = true
		}
		return NameLookupPage{}, err
	}
	if res.status != http.StatusOK {
		l.failed = true
		return NameLookupPage{}, rdProtocol("unexpected name lookup status")
	}
	page, err := names.ParsePage(res.body)
	if err != nil {
		return NameLookupPage{}, l.nmFail("invalid names page")
	}
	l.pages++
	for _, record := range page.Names {
		if names.Verify(record, l.client.now()) != nil || record.DisplayName == nil || !names.SameName(*record.DisplayName, l.query) {
			return NameLookupPage{}, l.nmFail("name lookup record failed verification or query")
		}
		if l.seenAuthors[record.AuthorID] || l.lastAuthor != "" && l.lastAuthor >= record.AuthorID {
			return NameLookupPage{}, l.nmFail("name lookup authors are not unique and ascending")
		}
		l.seenAuthors[record.AuthorID] = true
		l.lastAuthor = record.AuthorID
	}
	if page.NextCursor != nil {
		if l.seenCursor[*page.NextCursor] {
			return NameLookupPage{}, l.nmFail("name lookup cursor repeated")
		}
		l.seenCursor[*page.NextCursor] = true
		l.cursor = *page.NextCursor
	} else {
		l.done = true
	}
	return NameLookupPage{Records: page.Names, Done: l.done}, nil
}

func (l *NameLookup) nmFail(message string) error {
	l.failed = true
	return rdProtocol(message)
}

// NameSource reports one holder's record or its public-read signal during resolution.
type NameSource struct {
	// Endpoint is the holder's normalized base URL.
	Endpoint string
	// NameID is the verified record returned by this holder, or empty when none was returned.
	NameID string
	// Signal classifies a public-read or transport error; zero means a successful read.
	Signal Signal
	// Behind reports whether this holder's record is older than the selected record.
	Behind bool
}

// NameView is the newest verified record known across the queried holders.
type NameView struct {
	// Record is the selected current record when Found is true.
	Record names.NameRecord
	// Found reports whether any holder returned a valid record.
	Found bool
	// Withdrawn reports whether the selected record has a null display name.
	Withdrawn bool
	// Fingerprint is the profile's short fingerprint for the requested author.
	Fingerprint string
	// Sources reports one result for each client in input order.
	Sources []NameSource
}

// ResolveName concurrently reads every holder and applies the current-record rule to all verified records known.
func ResolveName(ctx context.Context, clients []*Client, authorID string, opts ResolveNameOptions) NameView {
	view := NameView{Fingerprint: names.Fingerprint(authorID), Sources: make([]NameSource, len(clients))}
	if _, err := keys.ParseDID(authorID); err != nil {
		for i, client := range clients {
			if client != nil {
				view.Sources[i] = NameSource{Endpoint: client.BaseURL(), Signal: SignalOther}
			}
		}
		return view
	}
	parallel := opts.Parallel
	if parallel <= 0 {
		parallel = 4
	}
	if parallel > len(clients) {
		parallel = len(clients)
	}
	if parallel == 0 {
		return view
	}
	type result struct {
		record names.NameRecord
		err    error
	}
	results := make([]result, len(clients))
	semaphore := make(chan struct{}, parallel)
	var wait sync.WaitGroup
	for i, client := range clients {
		wait.Add(1)
		go func(i int, client *Client) {
			defer wait.Done()
			if client == nil {
				results[i].err = errors.New("sdk: nil client")
				return
			}
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			results[i].record, results[i].err = client.Name(ctx, authorID)
		}(i, client)
	}
	wait.Wait()
	valid := make([]names.NameRecord, 0, len(clients))
	for i, result := range results {
		if clients[i] != nil {
			view.Sources[i].Endpoint = clients[i].BaseURL()
		}
		if result.err == nil {
			view.Sources[i].NameID = result.record.NameID
			valid = append(valid, result.record)
		} else {
			view.Sources[i].Signal = Classify(result.err)
		}
	}
	var currentNow time.Time
	clockSet := false
	for _, client := range clients {
		if client != nil {
			clientNow := client.now()
			if !clockSet || clientNow.After(currentNow) {
				currentNow = clientNow
				clockSet = true
			}
		}
	}
	if clockSet {
		view.Record, view.Found = names.Current(authorID, valid, currentNow)
	}
	if view.Found {
		view.Withdrawn = view.Record.DisplayName == nil
		for i, result := range results {
			if result.err == nil {
				view.Sources[i].Behind = result.record.NameID != view.Record.NameID && names.Newer(view.Record, result.record)
			}
		}
	}
	return view
}

// ResolveNameOptions controls bounded concurrency for cross-holder resolution.
type ResolveNameOptions struct {
	// Parallel bounds simultaneous holder requests; zero or less uses four.
	Parallel int
}
