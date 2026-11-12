package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

const (
	// nrPageTimeout bounds the reads made for one page handler call.
	nrPageTimeout = 10 * time.Second
	// nrJobTimeout bounds one run of a background job.
	nrJobTimeout = 60 * time.Second
	// nrFanout and nrLookupPages bound one lookup: docs/05-portability.md
	// requires clients to bound provider fanout for advisory lookups.
	nrFanout      = 4
	nrLookupPages = 3
	nrSyncPages   = 10
	nrSyncLimit   = 100
	// nrRecheckBatch bounds the proof lookups of one recheck run.
	nrRecheckBatch = 200
	// nrProofInterval spaces proof lookups for an unsettled record read again;
	// nrProofPoll spaces the background lookups that no deadline forces.
	nrProofInterval = time.Hour
	nrProofPoll     = 6 * time.Hour
	nrDefaultLimit  = 20
	nrMaxLimit      = 100
)

func init() {
	backgroundJobs = append(backgroundJobs, nrSuccessionJob, nrSyncJob, nrRecheckJob)
}

// nrSource is one place a profile reads records from.
type nrSource struct {
	endpoint Endpoint
	kind     sdk.SourceKind
}

func (src nrSource) label() string {
	if src.endpoint.Label != "" {
		return src.endpoint.Label
	}
	return src.endpoint.URL
}

func (src nrSource) status(ok bool, text string) SourceStatus {
	return SourceStatus{Label: src.label(), Endpoint: src.endpoint.URL, Kind: src.kind, OK: ok, Text: text}
}

func (src nrSource) failure(err error) SourceStatus {
	status := src.status(false, nrSignalText(err))
	status.Signal = sdk.Classify(err)
	return status
}

// nrSourcesFor lists what the profile selected, in read order: providers,
// then mirrors, then the vault. No other source is ever contacted for it.
func (a *App) nrSourcesFor(profile string) []nrSource {
	selection := a.selection(profile)
	sources := make([]nrSource, 0, len(selection.Providers)+len(selection.Mirrors)+1)
	for _, choice := range selection.Providers {
		sources = append(sources, nrSource{endpoint: Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label}, kind: sdk.SourceProvider})
	}
	for _, mirror := range selection.Mirrors {
		sources = append(sources, nrSource{endpoint: mirror, kind: sdk.SourceMirror})
	}
	if selection.Vault {
		vault := a.config.Foundation
		if vault.Label == "" {
			vault.Label = "Lifetime Vault"
		}
		sources = append(sources, nrSource{endpoint: vault, kind: sdk.SourceVault})
	}
	return sources
}

// nrAllSources is every source any profile selected, for background jobs.
func (a *App) nrAllSources() []nrSource {
	profiles := []string{""}
	if dirs, err := os.ReadDir(a.profiles.root); err == nil {
		for _, dir := range dirs {
			if dir.IsDir() && validProfileID(dir.Name()) {
				profiles = append(profiles, dir.Name())
			}
		}
	}
	var sources []nrSource
	seen := make(map[string]bool)
	for _, profile := range profiles {
		for _, src := range a.nrSourcesFor(profile) {
			key := src.endpoint.URL + "\x00" + src.endpoint.ServiceID
			if !seen[key] {
				seen[key] = true
				sources = append(sources, src)
			}
		}
	}
	return sources
}

// nrSignalText says what a failed read means at that one source.
func nrSignalText(err error) string {
	switch sdk.Classify(err) {
	case sdk.SignalNotFound:
		return "not found here"
	case sdk.SignalRetentionExpired:
		return "retention expired here"
	case sdk.SignalRateLimited:
		var answered *sdk.Error
		if errors.As(err, &answered) && answered.RetryAfter > 0 {
			return fmt.Sprintf("rate limited here; retry after %d s", int(answered.RetryAfter.Seconds()))
		}
		return "rate limited here"
	case sdk.SignalUnavailable:
		return "unavailable now"
	case sdk.SignalPolicyRestricted:
		return "restricted by policy here"
	case sdk.SignalLegalRestricted:
		return "restricted for legal reasons here"
	case sdk.SignalRestricted:
		return "restricted here"
	case sdk.SignalTimeout:
		return "did not answer in time"
	case sdk.SignalInvalidQuery:
		return "rejected the query"
	case sdk.SignalInvalidCursor:
		return "lost the listing position"
	case sdk.SignalProtocol:
		return "sent a malformed response"
	}
	return "unreachable"
}

func nrLimit(limit int) int {
	if limit <= 0 {
		return nrDefaultLimit
	}
	return min(limit, nrMaxLimit)
}

func nrEncodePosition(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func nrDecodePosition(position string) (string, error) {
	key, err := base64.RawURLEncoding.DecodeString(position)
	if err != nil || len(key) == 0 {
		return "", errRecordPosition
	}
	return string(key), nil
}

// nrFound is a record located for one read and where it came from.
type nrFound struct {
	view     post
	message  wire.TransportMessage
	endpoint Endpoint
	ok       bool
}

func (s *networkRecordSource) Get(ctx context.Context, r *http.Request, id string) (post, Coverage, bool, error) {
	if !canonicalMessageID(id) {
		p, ok, err := s.fallbackPost(ctx, r, id)
		if ok {
			return p, demoCoverage, true, err
		}
		return post{}, Coverage{Note: "Not a message ID, so no source was asked"}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, nrPageTimeout)
	defer cancel()
	found, coverage := s.locate(ctx, r, id)
	return found.view, coverage, found.ok, nil
}

// locate reads one record from the local copy, then from each selected source
// in order, stopping at the first that returns a verified record. A source
// that refuses or lacks it never ends the search.
func (s *networkRecordSource) locate(ctx context.Context, r *http.Request, id string) (nrFound, Coverage) {
	rd := s.reader(r)
	sources := s.app.nrSourcesFor(requestProfile(r))
	var coverage Coverage
	if entry, ok := s.store.get(id); ok {
		local := SourceStatus{Label: "Local copy", OK: true, Text: "held by this server since " + entry.FetchedAt.UTC().Format("2006-01-02")}
		rec := rd.verifier.Verify(entry.Raw, id, entry.source())
		if view, shown := rd.unverifiable(rec, entry.message); shown {
			return nrFound{view: view, message: entry.message, endpoint: nrReadEndpoint(sources, entry.Source.Endpoint), ok: true}, Coverage{Sources: []SourceStatus{local}}
		}
		if rec.Class != sdk.ClassInvalid {
			if rec = rd.verifier.Settle(rec, entry.Proof, nil); rec.Settlement != settlement.StateSettled && !rd.now.Before(entry.ProofCheckedAt.Add(nrProofInterval)) {
				rec = s.settleFresh(ctx, rd.verifier, sources, rec, entry.Proof)
				_ = s.store.settle(rec, rd.now)
			}
			return nrFound{view: postFromRecord(rd.verifier.Admit(rec), rd.now), message: entry.message, endpoint: nrReadEndpoint(sources, entry.Source.Endpoint), ok: true}, Coverage{Sources: []SourceStatus{local}}
		}
		s.store.remove(id)
	}
	for _, src := range sources {
		client, err := s.app.providerClient(src.endpoint)
		if err != nil {
			coverage.Sources = append(coverage.Sources, src.failure(err))
			continue
		}
		fetched, err := client.GetMessage(ctx, id)
		if err != nil {
			var answered *sdk.Error
			if src.kind == sdk.SourceProvider && !errors.As(err, &answered) {
				s.app.setProviderState(src.endpoint, err)
			}
			coverage.Sources = append(coverage.Sources, src.failure(err))
			continue
		}
		rec := rd.verifier.Verify(fetched.Raw, id, sdk.Source{Endpoint: fetched.Endpoint, ServiceID: fetched.ServiceID, Kind: src.kind})
		if view, shown := rd.unverifiable(rec, fetched.Message); shown {
			coverage.Sources = append(coverage.Sources, src.status(true, "returned the record; it cannot be fully checked without the trust root"))
			return nrFound{view: view, message: fetched.Message, endpoint: src.endpoint, ok: true}, coverage
		}
		if rec.Class == sdk.ClassInvalid {
			coverage.Sources = append(coverage.Sources, src.status(false, "returned a record that failed verification: "+rec.Failure.Name))
			continue
		}
		coverage.Sources = append(coverage.Sources, src.status(true, "returned the record"))
		rec = s.settleFresh(ctx, rd.verifier, sources, rec, nil)
		view := s.keep(rd, rec)
		_ = s.store.settle(rec, rd.now)
		return nrFound{view: view, message: fetched.Message, endpoint: src.endpoint, ok: true}, coverage
	}
	coverage.Note = "No selected source returned this record. That is not proof that it does not exist."
	return nrFound{}, coverage
}

// nrReadEndpoint picks the provider to ask about a held record: the one that
// served it when the reader still selects it, otherwise the first selected.
func nrReadEndpoint(sources []nrSource, served string) Endpoint {
	var first *Endpoint
	for i := range sources {
		if sources[i].kind != sdk.SourceProvider {
			continue
		}
		if sources[i].endpoint.URL == served {
			return sources[i].endpoint
		}
		if first == nil {
			first = &sources[i].endpoint
		}
	}
	if first == nil {
		return Endpoint{}
	}
	return *first
}

// settleFresh looks the settlement proof up at the admitting provider, when it
// is a selected source, then at the vault, and settles the record with the
// first proof that verifies. A held proof is the last resort, so a committee
// change is judged on the providers' current witnesses where they answer.
func (s *networkRecordSource) settleFresh(ctx context.Context, verifier *sdk.Verifier, sources []nrSource, rec sdk.Record, held *wire.SettlementProof) sdk.Record {
	var order []nrSource
	for _, src := range sources {
		if src.kind == sdk.SourceProvider && src.endpoint.ServiceID == rec.Receipt.ProviderID {
			order = append(order, src)
			break
		}
	}
	for _, src := range sources {
		if src.kind == sdk.SourceVault {
			order = append(order, src)
		}
	}
	for _, src := range order {
		client, err := s.app.providerClient(src.endpoint)
		if err != nil {
			continue
		}
		proof, _, err := client.Settlement(ctx, rec.Receipt.ReceiptID)
		if err != nil {
			continue
		}
		if settled := verifier.Settle(rec, &proof, nil); settled.Settlement == settlement.StateSettled {
			return settled
		}
	}
	return verifier.Settle(rec, held, nil)
}

// heldProof returns the proof the local copy holds for a record, if any.
func (s *networkRecordSource) heldProof(id string) *wire.SettlementProof {
	if entry, ok := s.store.get(id); ok {
		return entry.Proof
	}
	return nil
}

// keep stores a verified record and returns its card, settled from the best
// source the local copy knows for it: the same bytes from a mirror never hide
// that the admitting provider served them. A record is locally admitted only
// once its exact representation is durably stored, so another receipt for a
// held message is shown without admission.
func (s *networkRecordSource) keep(rd recordReader, rec sdk.Record) post {
	stored, err := s.store.put(rec, rd.now, false)
	if err != nil {
		s.app.logger().Printf("client record store: %v", err)
	}
	if err != nil || !stored {
		return postFromRecord(rec, rd.now)
	}
	if entry, ok := s.store.get(rec.ID); ok && entry.Source.Endpoint != rec.Source.Endpoint {
		rec.Source = entry.source()
		rec = rd.verifier.Settle(rec, entry.Proof, nil)
	}
	return postFromRecord(rd.verifier.Admit(rec), rd.now)
}

// show verifies one fetched record with any proof already held, keeps it when
// valid and returns its card.
func (s *networkRecordSource) show(rd recordReader, fetched sdk.Fetched, kind sdk.SourceKind) (post, bool) {
	rec := rd.verifier.Verify(fetched.Raw, fetched.ID, sdk.Source{Endpoint: fetched.Endpoint, ServiceID: fetched.ServiceID, Kind: kind})
	if view, shown := rd.unverifiable(rec, fetched.Message); shown {
		return view, true
	}
	if rec.Class == sdk.ClassInvalid {
		return post{}, false
	}
	return s.keep(rd, rd.verifier.Settle(rec, s.heldProof(rec.ID), nil)), true
}

// heldView shows one record from the local copy, dropping it when it no longer verifies.
func (s *networkRecordSource) heldView(rd recordReader, entry nrEntry) (post, bool) {
	rec := rd.verifier.Verify(entry.Raw, entry.message.MessageID, entry.source())
	if view, shown := rd.unverifiable(rec, entry.message); shown {
		return view, true
	}
	if rec.Class == sdk.ClassInvalid {
		s.store.remove(entry.message.MessageID)
		return post{}, false
	}
	return postFromRecord(rd.verifier.Admit(rd.verifier.Settle(rec, entry.Proof, nil)), rd.now), true
}

// nrSelectedHeld matches held records this reader may see: ones published here
// or served by a source the reader selected.
func nrSelectedHeld(sources []nrSource) func(*nrEntry) bool {
	selected := make(map[string]bool, len(sources))
	for _, src := range sources {
		selected[src.endpoint.URL] = true
	}
	return func(entry *nrEntry) bool {
		if entry.Mine {
			return true
		}
		for _, endpoint := range entry.Seen {
			if selected[endpoint] {
				return true
			}
		}
		return false
	}
}

func (s *networkRecordSource) Recent(ctx context.Context, r *http.Request, position string, limit int) (recordPage, error) {
	var afterTime, afterID string
	if position != "" {
		key, err := nrDecodePosition(position)
		if err != nil {
			return recordPage{}, err
		}
		var ok bool
		if afterTime, afterID, ok = strings.Cut(key, " "); !ok || !canonicalMessageID(afterID) {
			return recordPage{}, errRecordPosition
		}
	}
	rd := s.reader(r)
	sources := s.app.nrSourcesFor(requestProfile(r))
	selected := nrSelectedHeld(sources)
	held := s.store.list(func(entry *nrEntry) bool {
		if position != "" && (entry.message.Timestamp > afterTime || entry.message.Timestamp == afterTime && entry.message.MessageID <= afterID) {
			return false
		}
		return selected(entry)
	})
	page := recordPage{Coverage: s.syncCoverage(sources)}
	page.Coverage.Note = "Records this server has fetched from your sources, newest first by signed time"
	limit = nrLimit(limit)
	for i, entry := range held {
		if len(page.Posts) == limit {
			last := held[i-1].message
			page.Next = nrEncodePosition(last.Timestamp + " " + last.MessageID)
			break
		}
		if view, ok := s.heldView(rd, entry); ok {
			page.Posts = append(page.Posts, view)
		}
	}
	return page, nil
}

// syncCoverage states what each source's change stream last gave this server.
func (s *networkRecordSource) syncCoverage(sources []nrSource) Coverage {
	now := s.app.now()
	var coverage Coverage
	for _, src := range sources {
		state := s.store.cursor(src.endpoint.ServiceID)
		switch {
		case state.LastRun.IsZero():
			coverage.Sources = append(coverage.Sources, src.status(false, "not synced yet"))
		case state.Failure != "":
			text := "last sync " + relativeTime(state.LastRun, now) + ": " + state.Failure
			if now.Before(state.SkipUntil) {
				text += "; next try after " + state.SkipUntil.UTC().Format("15:04:05") + " UTC"
			}
			coverage.Sources = append(coverage.Sources, src.status(false, text))
		default:
			text := fmt.Sprintf("synced %s: %d records kept, %d failed verification", relativeTime(state.LastRun, now), state.Stored, state.Rejected)
			if state.More {
				text += "; more remain"
			}
			coverage.Sources = append(coverage.Sources, src.status(true, text))
		}
	}
	return coverage
}

func (s *networkRecordSource) Replies(ctx context.Context, r *http.Request, id string) (recordPage, error) {
	if !canonicalMessageID(id) {
		if _, ok, _ := s.fallbackPost(ctx, r, id); ok {
			return s.demo.Replies(ctx, r, id)
		}
		return recordPage{}, errors.New("not a message ID")
	}
	route, err := sdk.RepliesRoute(id)
	if err != nil {
		return recordPage{}, err
	}
	page := s.lookup(ctx, r, route, func(entry *nrEntry) bool { return entry.message.Payload.ReplyTo == id })
	slices.Reverse(page.Posts)
	return page, nil
}

func (s *networkRecordSource) Successors(ctx context.Context, r *http.Request, id string) (recordPage, error) {
	if !canonicalMessageID(id) {
		if _, ok, _ := s.fallbackPost(ctx, r, id); ok {
			return s.demo.Successors(ctx, r, id)
		}
		return recordPage{}, errors.New("not a message ID")
	}
	route, err := sdk.VersionsRoute(id)
	if err != nil {
		return recordPage{}, err
	}
	return s.lookup(ctx, r, route, func(entry *nrEntry) bool { return entry.message.Payload.Supersedes == id }), nil
}

func (s *networkRecordSource) ByAuthor(ctx context.Context, r *http.Request, authorID string) (recordPage, error) {
	route, err := sdk.AuthorRoute(authorID)
	if err != nil {
		return recordPage{}, err
	}
	return s.lookup(ctx, r, route, func(entry *nrEntry) bool { return entry.message.AuthorID == authorID }), nil
}

// lookup asks at most nrFanout selected sources, nrLookupPages pages each, and
// merges the held records that match, newest first. It runs only when a
// handler asks, which is a user action: lookups reveal the reader's interest,
// so they are never prefetched. A source without the optional route answers
// 404 and the local copy stands in for it.
func (s *networkRecordSource) lookup(ctx context.Context, r *http.Request, route sdk.Route, match func(*nrEntry) bool) recordPage {
	ctx, cancel := context.WithTimeout(ctx, nrPageTimeout)
	defer cancel()
	rd := s.reader(r)
	sources := s.app.nrSourcesFor(requestProfile(r))
	var page recordPage
	seen := make(map[string]bool)
	for _, src := range sources[:min(len(sources), nrFanout)] {
		messages, err := s.scan(ctx, src.endpoint, route, nrLookupPages)
		kept, rejected := 0, 0
		for _, fetched := range messages {
			if seen[fetched.ID] {
				continue
			}
			view, ok := s.show(rd, fetched, src.kind)
			if !ok {
				rejected++
				continue
			}
			seen[fetched.ID] = true
			page.Posts = append(page.Posts, view)
			kept++
		}
		text := fmt.Sprintf("returned %d verified records", kept)
		if rejected > 0 {
			text += fmt.Sprintf(", %d failed verification", rejected)
		}
		switch {
		case sdk.Classify(err) == sdk.SignalNotFound:
			page.Coverage.Sources = append(page.Coverage.Sources, src.status(false, "does not offer this lookup"))
		case err != nil:
			status := src.failure(err)
			status.Text = text + "; then " + status.Text
			page.Coverage.Sources = append(page.Coverage.Sources, status)
		default:
			page.Coverage.Sources = append(page.Coverage.Sources, src.status(true, text))
		}
	}
	held := 0
	selected := nrSelectedHeld(sources)
	for _, entry := range s.store.list(func(entry *nrEntry) bool { return match(entry) && !seen[entry.message.MessageID] && selected(entry) }) {
		if view, ok := s.heldView(rd, entry); ok {
			page.Posts = append(page.Posts, view)
			held++
		}
	}
	page.Coverage.Sources = append(page.Coverage.Sources, SourceStatus{Label: "Local copy", OK: true, Text: fmt.Sprintf("%d more held records match", held)})
	if len(sources) > nrFanout {
		page.Coverage.Note = fmt.Sprintf("Asked the first %d of your %d sources", nrFanout, len(sources))
	}
	slices.SortStableFunc(page.Posts, func(a, b post) int {
		if a.Timestamp != b.Timestamp {
			return strings.Compare(b.Timestamp, a.Timestamp)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return page
}

func (s *networkRecordSource) Search(ctx context.Context, r *http.Request, query string, limit int) (recordPage, error) {
	page := recordPage{Coverage: Coverage{Note: "Searching records this server has fetched"}}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return page, nil
	}
	rd := s.reader(r)
	selected := nrSelectedHeld(s.app.nrSourcesFor(requestProfile(r)))
	limit = nrLimit(limit)
	for _, entry := range s.store.list(func(entry *nrEntry) bool {
		return selected(entry) && strings.Contains(strings.ToLower(entry.message.Payload.Text), needle)
	}) {
		if len(page.Posts) == limit {
			break
		}
		if view, ok := s.heldView(rd, entry); ok {
			page.Posts = append(page.Posts, view)
		}
	}
	return page, nil
}

// keepPublished stores a record this client published, so it is never evicted.
func (s *networkRecordSource) keepPublished(profile string, published sdk.Published, client *sdk.Client) error {
	verifier := s.app.verifier(profile)
	rec := verifier.Verify(published.Raw, published.MessageID, sdk.Source{Endpoint: client.BaseURL(), ServiceID: client.ServiceID(), Kind: sdk.SourceProvider})
	if rec.Class == sdk.ClassInvalid {
		return fmt.Errorf("published record failed local verification: %s", rec.Failure.Name)
	}
	stored, err := s.store.put(verifier.Settle(rec, nil, nil), s.app.now(), true)
	if err != nil {
		return err
	}
	if !stored {
		return errors.New("another receipt for this message is already held")
	}
	return nil
}

func (a *App) networkSource() (*networkRecordSource, bool) {
	s, ok := a.recordSource.(*networkRecordSource)
	return s, ok
}

// providerSuccessions are the verified key successions of the selected providers.
func (a *App) providerSuccessions() []wire.KeySuccession {
	if s, ok := a.networkSource(); ok {
		return s.store.allSuccessions()
	}
	return nil
}

// nrSuccessionJob fetches each selected provider's key successions at most once
// a day, so the delinquency line follows a provider key succession
// (docs/02-protocol.md Key succession).
func nrSuccessionJob(ctx context.Context, a *App) error {
	s, ok := a.networkSource()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, nrJobTimeout)
	defer cancel()
	now := a.now()
	var failures []error
	for _, src := range a.nrAllSources() {
		providerID := src.endpoint.ServiceID
		if src.kind != sdk.SourceProvider || !s.store.successionsDue(providerID, now) {
			continue
		}
		s.store.touchSuccessions(providerID, now)
		client, err := a.providerClient(src.endpoint)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		items, _, _, err := client.Successions(ctx, providerID)
		if err != nil {
			failures = append(failures, fmt.Errorf("successions from %s: %w", src.label(), err))
			continue
		}
		verified := make([]wire.KeySuccession, 0, len(items))
		for _, item := range items {
			if succession.Verify(item) == nil {
				verified = append(verified, item)
			}
		}
		s.store.setSuccessions(providerID, verified, now)
	}
	return errors.Join(failures...)
}

// nrSyncJob follows each selected source's change stream from the next_after
// stored for its provider_id, verifies every item and keeps the valid ones.
// Sequences belong to the source that assigned them, and a consumed stream is
// not a completeness proof (docs/05-portability.md Incremental sync).
func nrSyncJob(ctx context.Context, a *App) error {
	s, ok := a.networkSource()
	if root, _ := a.trustRoot(); !ok || root == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, nrJobTimeout)
	defer cancel()
	verifier := a.verifier("")
	var failures []error
	for _, src := range a.nrAllSources() {
		if err := s.syncSource(ctx, verifier, src); err != nil {
			failures = append(failures, fmt.Errorf("sync %s: %w", src.label(), err))
		}
	}
	return errors.Join(failures...)
}

func (s *networkRecordSource) syncSource(ctx context.Context, verifier *sdk.Verifier, src nrSource) error {
	now := s.app.now()
	providerID := src.endpoint.ServiceID
	state := s.store.cursor(providerID)
	if now.Before(state.SkipUntil) {
		return nil
	}
	client, err := s.app.providerClient(src.endpoint)
	if err != nil {
		return err
	}
	state.LastRun, state.Stored, state.Rejected, state.More, state.Failure = now, 0, 0, false, ""
	var failure error
	for range nrSyncPages {
		result, err := client.Changes(ctx, state.NextAfter, nrSyncLimit)
		if err != nil {
			state.Failure = nrSignalText(err)
			var answered *sdk.Error
			if errors.As(err, &answered) && answered.RetryAfter > 0 {
				state.SkipUntil = now.Add(answered.RetryAfter)
			}
			break
		}
		raws, err := nrChangeMessages(result.Raw)
		if err != nil {
			state.Failure = "sent a malformed response"
			break
		}
		for i, item := range result.Page.Items {
			rec := verifier.Verify(raws[i], item.Message.MessageID, sdk.Source{Endpoint: client.BaseURL(), ServiceID: client.ServiceID(), Kind: src.kind})
			if rec.Class == sdk.ClassInvalid {
				state.Rejected++
				continue
			}
			stored, err := s.store.put(verifier.Settle(rec, s.heldProof(rec.ID), nil), now, false)
			if err != nil {
				failure = err
				break
			}
			if stored {
				state.Stored++
			}
		}
		if failure != nil {
			state.Failure = "could not store records"
			break
		}
		state.NextAfter, state.More = uint64(result.Page.NextAfter), result.Page.More
		if !state.More {
			break
		}
	}
	return errors.Join(failure, s.store.setCursor(providerID, state))
}

// nrChangeMessages returns the exact bytes of each message in a changes page.
func nrChangeMessages(raw []byte) ([][]byte, error) {
	var exact struct {
		Items []struct {
			Message json.RawMessage `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &exact); err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(exact.Items))
	for _, item := range exact.Items {
		out = append(out, item.Message)
	}
	return out, nil
}

// nrRecheckJob refreshes the settlement evidence of held records. A record
// pending from its provider's own routes must be rechecked no later than 7
// days after its receipt was issued; a receipt still unsettled then is
// delinquent, and the verifier treats none of that provider's receipts as
// pending until it settles. A settled record is re-evaluated when the
// committee policy changes (docs/03-economics.md Proofs and receipt states).
// Those rechecks go first; the rest of the batch looks for proofs of other
// unsettled records, so a post can show Settled before its deadline. RunOnce
// saves the delinquency ledger afterwards.
func nrRecheckJob(ctx context.Context, a *App) error {
	s, ok := a.networkSource()
	if root, _ := a.trustRoot(); !ok || root == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, nrJobTimeout)
	defer cancel()
	verifier := a.verifier("")
	sources := a.nrAllSources()
	now := a.now()
	var due, polls []nrEntry
	for _, entry := range s.store.list(func(*nrEntry) bool { return true }) {
		held := sdk.Record{Receipt: entry.message.SubmissionReceipt, Settlement: settlement.State(entry.Settlement), RecheckBy: entry.RecheckBy, SettledUnderPolicy: entry.SettledUnderPolicy}
		switch {
		case verifier.NeedsRecheck(held):
			due = append(due, entry)
		case held.Settlement != settlement.StateSettled && !now.Before(entry.ProofCheckedAt.Add(nrProofPoll)):
			polls = append(polls, entry)
		}
	}
	due = append(due, polls...)
	var failures []error
	for _, entry := range due[:min(len(due), nrRecheckBatch)] {
		rec := verifier.Verify(entry.Raw, entry.message.MessageID, entry.source())
		if rec.Class == sdk.ClassInvalid {
			s.store.remove(entry.message.MessageID)
			continue
		}
		if err := s.store.settle(s.settleFresh(ctx, verifier, sources, rec, entry.Proof), now); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
