package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

// networkReadTimeout bounds the provider reads made while answering one page,
// so a dead provider never hangs the reader.
const networkReadTimeout = 2 * time.Second

const (
	networkListPages   = 3
	networkThreadReads = 20
	networkThreadNodes = 200
	networkCacheLimit  = 500
)

var errNetworkPublish = errors.New("Publishing new posts to the network is not available in this client yet.")

// networkRecordSource reads verified records from the reader's selected
// providers. In dev mode it shows the demo source, marked, while no provider
// lists any post.
type networkRecordSource struct {
	app  *App
	demo *demoRecordSource
	mu   sync.Mutex
	// fetched keeps exact message bytes by ID; they are verified again on every use.
	fetched map[string]sdk.Fetched
	quotes  map[string]networkReplyQuote
}

type networkReplyQuote struct {
	quoted  sdk.Quoted
	key     ed25519.PrivateKey
	client  *sdk.Client
	profile string
	draft   replyDraft
	expires time.Time
}

var newRecordSource = func(a *App) recordSource {
	demo := &demoRecordSource{app: a}
	if len(a.config.Providers) > 0 {
		return &networkRecordSource{app: a, demo: demo}
	}
	return demo
}

// requestProfile reads the reader's profile ID without creating one.
func requestProfile(r *http.Request) string {
	if r != nil {
		if cookie, err := r.Cookie(profileCookie); err == nil && validProfileID(cookie.Value) {
			return cookie.Value
		}
	}
	return ""
}

// recordReader verifies records for one request under the reader's provider policy.
type recordReader struct {
	verifier *sdk.Verifier
	root     *sdk.TrustRoot
	now      time.Time
}

func (s *networkRecordSource) reader(r *http.Request) recordReader {
	root, _ := s.app.trustRoot()
	return recordReader{verifier: s.app.verifier(requestProfile(r)), root: root, now: s.app.now()}
}

// view verifies one fetched message. A record that fails verification is not
// shown. While no trust root is available, a record whose signatures verified
// is shown as not verifiable now: only the schedule and fee step needs the root.
func (rd recordReader) view(fetched sdk.Fetched) (post, bool) {
	record := rd.verifier.Verify(fetched.Raw, fetched.ID, sdk.Source{Endpoint: fetched.Endpoint, ServiceID: fetched.ServiceID, Kind: sdk.SourceProvider})
	if record.Class != sdk.ClassInvalid {
		return postFromRecord(rd.verifier.Admit(rd.verifier.Settle(record, nil, nil)), rd.now), true
	}
	if rd.root != nil || record.Failure == nil || record.Failure.Step != 5 {
		return post{}, false
	}
	p := postFromRecord(sdk.Record{Message: fetched.Message}, rd.now)
	p.Unverifiable = "The trust root is unavailable, so the fee schedule cannot be checked: " + record.Failure.Detail
	return p, true
}

func (s *networkRecordSource) endpoints(r *http.Request) []Endpoint {
	choices := s.app.selection(requestProfile(r)).Providers
	endpoints := make([]Endpoint, 0, len(choices))
	for _, choice := range choices {
		endpoints = append(endpoints, Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label})
	}
	return endpoints
}

func (s *networkRecordSource) remember(fetched sdk.Fetched) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetched == nil {
		s.fetched = make(map[string]sdk.Fetched)
	}
	if _, ok := s.fetched[fetched.ID]; ok || len(s.fetched) < networkCacheLimit {
		s.fetched[fetched.ID] = fetched
	}
}

func (s *networkRecordSource) cached(id string) (sdk.Fetched, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fetched, ok := s.fetched[id]
	return fetched, ok
}

// scan lists one route of one provider, bounded in pages.
func (s *networkRecordSource) scan(ctx context.Context, endpoint Endpoint, route sdk.Route, pages int) ([]sdk.Fetched, error) {
	client, err := s.app.providerClient(endpoint)
	if err != nil {
		return nil, err
	}
	pager, err := sdk.NewPager(client, route, sdk.ListQuery{Limit: 50})
	if err != nil {
		return nil, err
	}
	pager.MaxPages = pages
	var messages []sdk.Fetched
	for range pages {
		page, err := pager.Next(ctx)
		if err != nil {
			return messages, err
		}
		messages = append(messages, page.Messages...)
		if page.Done {
			break
		}
	}
	return messages, nil
}

func (s *networkRecordSource) Posts(ctx context.Context, r *http.Request) ([]post, error) {
	ctx, cancel := context.WithTimeout(ctx, networkReadTimeout)
	defer cancel()
	rd := s.reader(r)
	var posts []post
	listed, reached := 0, false
	seen := make(map[string]bool)
	for _, endpoint := range s.endpoints(r) {
		messages, err := s.scan(ctx, endpoint, sdk.MessagesRoute(), networkListPages)
		s.app.setProviderState(endpoint, err)
		reached = reached || err == nil
		listed += len(messages)
		for _, fetched := range messages {
			if seen[fetched.ID] {
				continue
			}
			seen[fetched.ID] = true
			s.remember(fetched)
			if p, ok := rd.view(fetched); ok {
				posts = append(posts, p)
			}
		}
	}
	if listed == 0 && s.app.config.Dev {
		note := "Provider has no posts yet; showing demo data"
		if !reached {
			note = "Provider unreachable; showing demo data"
		}
		s.app.setFallback(true, note)
		demo, err := s.demo.Posts(ctx, r)
		for i := range demo {
			demo[i].DemoData = true
		}
		return demo, err
	}
	s.app.setFallback(false, "")
	slices.SortStableFunc(posts, func(a, b post) int {
		if a.Timestamp != b.Timestamp {
			return strings.Compare(b.Timestamp, a.Timestamp)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return posts, nil
}

// fallbackPost answers a demo post ID while the dev demo fallback is showing.
func (s *networkRecordSource) fallbackPost(ctx context.Context, r *http.Request, id string) (post, bool, error) {
	if !s.app.config.Dev || !s.app.fallbackActive() {
		return post{}, false, nil
	}
	p, ok, err := s.demo.Post(ctx, r, id)
	p.DemoData = ok
	return p, ok, err
}

// fetch returns a message by ID from the cache or the first selected provider that has it.
func (s *networkRecordSource) fetch(ctx context.Context, r *http.Request, id string) (sdk.Fetched, Endpoint, bool) {
	endpoints := s.endpoints(r)
	if fetched, ok := s.cached(id); ok {
		for _, endpoint := range endpoints {
			if endpoint.URL == fetched.Endpoint {
				return fetched, endpoint, true
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, networkReadTimeout)
	defer cancel()
	for _, endpoint := range endpoints {
		client, err := s.app.providerClient(endpoint)
		if err != nil {
			continue
		}
		fetched, err := client.GetMessage(ctx, id)
		if err != nil {
			var answered *sdk.Error
			if !errors.As(err, &answered) {
				s.app.setProviderState(endpoint, err)
			}
			continue
		}
		s.remember(fetched)
		return fetched, endpoint, true
	}
	return sdk.Fetched{}, Endpoint{}, false
}

func (s *networkRecordSource) Post(ctx context.Context, r *http.Request, id string) (post, bool, error) {
	if !canonicalMessageID(id) {
		return s.fallbackPost(ctx, r, id)
	}
	fetched, _, found := s.fetch(ctx, r, id)
	if !found {
		return post{}, false, nil
	}
	p, ok := s.reader(r).view(fetched)
	return p, ok, nil
}

func (s *networkRecordSource) Thread(ctx context.Context, r *http.Request, postID string) ([]reply, error) {
	if !canonicalMessageID(postID) {
		if _, ok, _ := s.fallbackPost(ctx, r, postID); ok {
			return s.demo.Thread(ctx, r, postID)
		}
		return nil, nil
	}
	root, endpoint, found := s.fetch(ctx, r, postID)
	if !found {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, networkReadTimeout)
	defer cancel()
	rd := s.reader(r)
	var candidates []wire.PostMessage
	views := make(map[string]post)
	queue, reads := []string{postID}, 0
	for len(queue) > 0 && reads < networkThreadReads && len(candidates) < networkThreadNodes {
		route, err := sdk.RepliesRoute(queue[0])
		if err != nil {
			return nil, err
		}
		queue, reads = queue[1:], reads+1
		messages, err := s.scan(ctx, endpoint, route, 1)
		if err != nil {
			break
		}
		for _, fetched := range messages {
			if _, ok := views[fetched.ID]; ok {
				continue
			}
			p, ok := rd.view(fetched)
			if !ok {
				continue
			}
			s.remember(fetched)
			views[fetched.ID] = p
			candidates = append(candidates, fetched.Message)
			queue = append(queue, fetched.ID)
		}
	}
	rootMessage := root.Message
	built := sdk.BuildThread(postID, &rootMessage, candidates, sdk.Limits{MaxDepth: replyDepthLimit, MaxNodes: networkThreadNodes})
	if built.Root == nil {
		return nil, nil
	}
	return networkReplies(built.Root.Children, views, postID, "", 1), nil
}

func networkReplies(nodes []*sdk.ThreadNode, views map[string]post, postID, parent string, depth int) []reply {
	if depth > replyDepthLimit || len(nodes) == 0 {
		return nil
	}
	out := make([]reply, 0, len(nodes))
	for i, node := range nodes {
		view := views[node.Message.MessageID]
		item := reply{ID: view.ID, Color: view.Color, Time: view.Time, Text: view.Text, Format: view.Format, Receipt: view.Receipt, PostID: postID, Path: childPath(parent, i), Open: depth < replyDepthLimit}
		item.HTML = renderNetworkText(view.Format, view.Text)
		item.Children = networkReplies(node.Children, views, postID, item.Path, depth+1)
		item.Descendants = countReplies(item.Children)
		out = append(out, item)
	}
	return out
}

// renderNetworkText renders a network reply in its own format. Media slots are
// left out, so no picture is fetched, and links stay inert.
func renderNetworkText(format, text string) template.HTML {
	rendered, err := content.Render(wire.PostPayload{ContentFormat: format, Text: text}, content.RenderOptions{})
	if err != nil {
		return template.HTML("<p>" + template.HTMLEscapeString(text) + "</p>")
	}
	return rendered.HTML
}

func (s *networkRecordSource) Pictures(ctx context.Context, r *http.Request, id, replyPath string) ([]int, string, bool, error) {
	if _, ok, err := s.fallbackPost(ctx, r, id); !ok || err != nil {
		return nil, "", false, err
	}
	return s.demo.Pictures(ctx, r, id, replyPath)
}

func (s *networkRecordSource) PublishPost(ctx context.Context, text, token string) (post, error) {
	if s.app.config.Dev && s.app.fallbackActive() {
		return s.demo.PublishPost(ctx, text, token)
	}
	return post{}, errNetworkPublish
}

func (s *networkRecordSource) QuoteReply(ctx context.Context, w http.ResponseWriter, r *http.Request, d replyDraft, now time.Time) (replyDraft, error) {
	if !canonicalMessageID(d.PostID) {
		return s.demo.QuoteReply(ctx, w, r, d, now)
	}
	root, status := s.app.trustRoot()
	if root == nil {
		return replyDraft{}, fmt.Errorf("the trust root is unavailable (%s), so no fee can be checked", status.Err)
	}
	profile, err := s.app.profile(w, r)
	if err != nil {
		return replyDraft{}, err
	}
	choice, ok := s.app.publishingProvider(profile)
	if !ok {
		return replyDraft{}, errors.New("no trusted provider is selected for publishing")
	}
	client, err := s.app.providerClient(Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label})
	if err != nil {
		return replyDraft{}, err
	}
	key, err := s.app.authorKey(w, r)
	if err != nil {
		return replyDraft{}, fmt.Errorf("no author key is available: %w", err)
	}
	parentID := d.PostID
	if d.Path != "" {
		replies, _ := s.Thread(ctx, r, d.PostID)
		chain := replyChain(replies, d.Path)
		if chain == nil || chain[len(chain)-1].ID == "" {
			return replyDraft{}, errors.New("the reply you are answering is no longer available")
		}
		parentID = chain[len(chain)-1].ID
	}
	journal, err := sdk.FileJournal(filepath.Join(s.app.config.DataDir, "publish", profile))
	if err != nil {
		return replyDraft{}, err
	}
	publisher := sdk.NewPublisher(client, root, journal, s.app.config.Now)
	quoted, err := publisher.QuoteDraft(ctx, sdk.Draft{ContentFormat: content.FormatMarkdown, Text: d.Text, ReplyTo: parentID}, key, publishNetwork(s.app, profile), nil)
	if err != nil {
		return replyDraft{}, err
	}
	d.Cost, err = quotedCost(quoted)
	if err != nil {
		return replyDraft{}, err
	}
	if d.Quote, err = draftToken(); err != nil {
		return replyDraft{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, kept := range s.quotes {
		if !now.Before(kept.expires) {
			delete(s.quotes, token)
		}
	}
	if len(s.quotes) >= demoQuoteLimit {
		return replyDraft{}, errDemoStoreFull
	}
	if s.quotes == nil {
		s.quotes = make(map[string]networkReplyQuote)
	}
	s.quotes[d.Quote] = networkReplyQuote{quoted: quoted, key: key, client: client, profile: profile, draft: d, expires: now.Add(replyQuoteLifetime)}
	return d, nil
}

// quotedCost shows the amount of the provider's signed quote, which the publisher checked against the schedule in force.
func quotedCost(q sdk.Quoted) (postCost, error) {
	amount, err := fee.ParseAmount(q.Quote.AmountBaseUnits)
	if err != nil {
		return postCost{}, err
	}
	label := feeLabels[q.Prepared.Network]
	if label.Asset == "" {
		label = networkFee{Asset: q.Prepared.Network, Unit: q.Prepared.Denomination}
	}
	label.Amount = groupDigits(fee.FormatAmount(amount))
	billable := q.Prepared.BillableBytes
	return postCost{Bytes: billable, Units: (billable + 1023) / 1024, Fees: []networkFee{label}}, nil
}

func publishNetwork(a *App, profile string) string {
	if settings, err := a.settings(profile); err == nil && settings.PublishNetwork == wire.EthereumMainnet {
		return wire.EthereumMainnet
	}
	return wire.BitcoinMainnet
}

func (s *networkRecordSource) AddReply(ctx context.Context, r *http.Request, token string, now time.Time) (replyDraft, bool, error) {
	s.mu.Lock()
	kept, ok := s.quotes[token]
	delete(s.quotes, token)
	s.mu.Unlock()
	if !ok {
		return s.demo.AddReply(ctx, r, token, now)
	}
	if kept.profile != requestProfile(r) || !now.Before(kept.expires) {
		return replyDraft{}, false, nil
	}
	root, _ := s.app.trustRoot()
	journal, err := sdk.FileJournal(filepath.Join(s.app.config.DataDir, "publish", kept.profile))
	if err != nil {
		return kept.draft, true, err
	}
	publisher := sdk.NewPublisher(kept.client, root, journal, s.app.config.Now)
	approval := sdk.Approval{MessageID: kept.quoted.Prepared.MessageID, Amount: kept.quoted.Quote.AmountBaseUnits, Network: kept.quoted.Quote.Network, ApprovedAt: now}
	_, err = publisher.Publish(ctx, kept.key, kept.quoted, approval)
	return kept.draft, true, err
}

// postFromRecord maps a record onto the shared post card. Media slots become
// inert lines: nothing is fetched while rendering.
func postFromRecord(record sdk.Record, now time.Time) post {
	m := record.Message
	kind := "text"
	var media strings.Builder
	if m.Payload.MediaSlots != nil {
		for _, slot := range *m.Payload.MediaSlots {
			if kind == "text" && slot.Kind == "image" {
				kind = "images"
			}
			if slot.Kind == "video" {
				kind = "video"
			}
			host := "unknown host"
			if parsed, err := url.Parse(slot.URL); err == nil && parsed.Hostname() != "" {
				host = parsed.Hostname()
			}
			alt := "no description"
			if slot.AltText != nil && *slot.AltText != "" {
				alt = *slot.AltText
			}
			fmt.Fprintf(&media, `<p class="post-media-inert"><span>%s</span><span>%s</span><span>%s</span><span>Not loaded</span></p>`, html.EscapeString(slot.Kind), html.EscapeString(host), html.EscapeString(alt))
		}
	}
	when, _ := wire.ParseTime(m.Timestamp)
	p := post{
		ID: m.MessageID, Text: m.Payload.Text, Format: m.Payload.ContentFormat, Kind: kind,
		IsReply: m.Payload.ReplyTo != "", AuthorID: m.AuthorID, Receipt: shortReceipt(m.SubmissionReceipt.ReceiptID),
		Time: relativeTime(when, now), Timestamp: m.Timestamp, Color: authorColor(m.AuthorID), Media: template.HTML(media.String()),
		Settlement: string(record.Settlement), Class: string(record.Class),
	}
	if !record.RecheckBy.IsZero() {
		p.RecheckBy = record.RecheckBy.UTC().Format("2006-01-02")
	}
	return p
}

func shortReceipt(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:7] + "…" + value[len(value)-4:]
}

// authorColor picks one of the five avatar colors from the author key.
func authorColor(did string) string {
	sum := sha256.Sum256([]byte(did))
	return []string{"blue", "mint", "peach", "lilac", "violet"}[int(sum[0])%5]
}

// relativeTime says how long ago something happened, or gives its UTC date after 30 days.
func relativeTime(when, now time.Time) string {
	if when.IsZero() {
		return "Unknown time"
	}
	age := max(now.Sub(when), 0)
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%d min ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return countAgo(int(age.Hours()), "hour")
	case age < 30*24*time.Hour:
		return countAgo(int(age.Hours()/24), "day")
	}
	return when.UTC().Format("2006-01-02")
}

func countAgo(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

// canonicalMessageID checks the shape of a message ID before any source is contacted.
func canonicalMessageID(id string) bool {
	if !strings.HasPrefix(id, "fy1:") || len(id) != 68 {
		return false
	}
	for _, c := range id[4:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
