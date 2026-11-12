package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"foryou.local/foryou/internal/sdk"
)

// SourceStatus is what one source answered during a read. A status describes
// that source only: a 403, 404 or 410 is never global absence or deletion.
type SourceStatus struct {
	// Label names the source as the reader selected it; Endpoint is its base URL.
	Label, Endpoint string
	// Kind is provider, mirror or vault; zero for this server's local copy or demo data.
	Kind sdk.SourceKind
	// OK is true when the source answered; Signal classifies the failure otherwise.
	OK     bool
	Signal sdk.Signal
	// Text says in words what the source answered, such as "restricted by policy here".
	Text string
}

// Coverage names every source a read consulted and what each said. It never
// states a network-wide total or proves that nothing else exists.
type Coverage struct {
	// Note describes the read as a whole, such as "Searching records this server has fetched".
	Note    string
	Sources []SourceStatus
}

// Lines renders the coverage as "Label: text" lines, the note first.
func (c Coverage) Lines() []string {
	lines := make([]string, 0, len(c.Sources)+1)
	if c.Note != "" {
		lines = append(lines, c.Note)
	}
	for _, source := range c.Sources {
		lines = append(lines, source.Label+": "+source.Text)
	}
	return lines
}

// String joins the coverage lines for a single line of text.
func (c Coverage) String() string { return strings.Join(c.Lines(), " · ") }

// recordPage is one bounded answer of a record source. Next is the opaque
// position of the following page of Recent, empty when no more records are held.
type recordPage struct {
	Posts    []post
	Next     string
	Coverage Coverage
}

// recordSource is the contract handlers use to read records and publish local reader actions.
type recordSource interface {
	// Posts returns posts visible to the current reader.
	Posts(ctx context.Context, r *http.Request) ([]post, error)
	// Post returns one post and whether this source has it.
	Post(ctx context.Context, r *http.Request, postID string) (post, bool, error)
	// Thread returns the prepared reply tree for a post.
	Thread(ctx context.Context, r *http.Request, postID string) ([]reply, error)
	// Pictures returns demo viewer images for a post or reply path.
	Pictures(ctx context.Context, r *http.Request, postID, replyPath string) ([]int, string, bool, error)
	// PublishPost publishes a demo post when the selected source supports it.
	PublishPost(ctx context.Context, text, token string) (post, error)
	// QuoteReply prices and stores a reply draft for confirmation.
	QuoteReply(ctx context.Context, w http.ResponseWriter, r *http.Request, draft replyDraft, now time.Time) (replyDraft, error)
	// AddReply consumes a confirmed quote once.
	AddReply(ctx context.Context, r *http.Request, token string, now time.Time) (replyDraft, bool, error)
	// Get returns one record by ID, trying the local copy, then the selected
	// providers in order, then mirrors, then the vault, with each source's answer.
	Get(ctx context.Context, r *http.Request, id string) (post, Coverage, bool, error)
	// Recent returns held records newest first by signed timestamp, from an
	// opaque position (empty for the first page), at most limit of them.
	Recent(ctx context.Context, r *http.Request, position string, limit int) (recordPage, error)
	// Replies returns the verified direct replies to a record, oldest first.
	Replies(ctx context.Context, r *http.Request, id string) (recordPage, error)
	// Successors returns verified records whose signed supersedes names id:
	// immediate successor candidates, not a verified chain.
	Successors(ctx context.Context, r *http.Request, id string) (recordPage, error)
	// ByAuthor returns verified records signed by one canonical author DID, newest first.
	ByAuthor(ctx context.Context, r *http.Request, authorID string) (recordPage, error)
	// Search matches text case-insensitively over records this server already
	// holds; it never queries the network.
	Search(ctx context.Context, r *http.Request, query string, limit int) (recordPage, error)
}

// demoRecordSource keeps the existing reader fixtures behind recordSource.
type demoRecordSource struct{ app *App }

func (s *demoRecordSource) Posts(context.Context, *http.Request) ([]post, error) {
	added := s.app.store.list()
	posts := make([]post, 0, len(added)+len(staticDemoPosts))
	posts = append(posts, added...)
	posts = append(posts, staticDemoPosts...)
	return posts, nil
}

func (s *demoRecordSource) Post(_ context.Context, _ *http.Request, id string) (post, bool, error) {
	if p, ok := s.app.store.get(id); ok {
		return p, true, nil
	}
	p, ok := postByID(id)
	return p, ok, nil
}

func (s *demoRecordSource) Thread(_ context.Context, r *http.Request, postID string) ([]reply, error) {
	replies := prepareReplies(demoReplies(postID), postID, "", replyDepthLimit)
	store := s.app.store
	if store == nil {
		return replies, nil
	}
	for _, added := range store.repliesTo(postID) {
		siblings := &replies
		depth := 1
		if added.parent != "" {
			chain := replyChain(replies, added.parent)
			if chain == nil {
				continue
			}
			for _, ancestor := range chain {
				ancestor.Descendants++
			}
			siblings = &chain[len(chain)-1].Children
			depth = len(chain) + 1
		}
		path := childPath(added.parent, len(*siblings))
		written := reply{Color: "violet", Time: "Just now", Text: added.text, Receipt: mockReceipt(postID + "/" + path + "/" + added.text), PostID: postID, Path: path, Mine: true, Open: depth < replyDepthLimit}
		written.render()
		*siblings = append(*siblings, written)
	}
	return replies, nil
}

func (s *demoRecordSource) Pictures(_ context.Context, r *http.Request, id, replyPath string) ([]int, string, bool, error) {
	p, found := s.app.requestPost(r, id)
	if !found {
		return nil, "", false, nil
	}
	if replyPath == "" {
		images, ok := demoImages[id]
		return images, p.Text, ok, nil
	}
	replies, _ := s.Thread(r.Context(), r, id)
	chain := replyChain(replies, replyPath)
	if chain == nil {
		return nil, "", false, nil
	}
	images := chain[len(chain)-1].pictures
	return images, "", len(images) > 0, nil
}

func (s *demoRecordSource) PublishPost(_ context.Context, text, token string) (post, error) {
	return s.app.store.add(text, token)
}

func (s *demoRecordSource) QuoteReply(_ context.Context, _ http.ResponseWriter, _ *http.Request, d replyDraft, now time.Time) (replyDraft, error) {
	cost, err := costOf(d.Text)
	if err != nil {
		return replyDraft{}, err
	}
	d.Cost = cost
	token, err := s.app.store.quoteReply(d, now)
	if err != nil {
		return replyDraft{}, err
	}
	d.Quote = token
	return d, nil
}

func (s *demoRecordSource) AddReply(_ context.Context, _ *http.Request, token string, now time.Time) (replyDraft, bool, error) {
	d, ok := s.app.store.takeQuote(token, now)
	if !ok {
		return replyDraft{}, false, nil
	}
	if err := s.app.store.addReply(d.PostID, d.Path, d.Text); err != nil {
		return replyDraft{}, true, err
	}
	return d, true, nil
}

var errRecordPosition = errors.New("this list position is not valid")

var demoCoverage = Coverage{Sources: []SourceStatus{{Label: "Demo data", OK: true, Text: "fixtures on this server, not network records"}}}

func (s *demoRecordSource) Get(ctx context.Context, r *http.Request, id string) (post, Coverage, bool, error) {
	p, ok, err := s.Post(ctx, r, id)
	return p, demoCoverage, ok, err
}

func (s *demoRecordSource) Recent(ctx context.Context, r *http.Request, position string, limit int) (recordPage, error) {
	posts, err := s.Posts(ctx, r)
	if err != nil {
		return recordPage{}, err
	}
	start := 0
	if position != "" {
		last, err := nrDecodePosition(position)
		if err != nil {
			return recordPage{}, err
		}
		start = -1
		for i, p := range posts {
			if p.ID == last {
				start = i + 1
			}
		}
		if start < 0 {
			return recordPage{}, errRecordPosition
		}
	}
	page := recordPage{Posts: posts[start:min(start+nrLimit(limit), len(posts))], Coverage: demoCoverage}
	if end := start + len(page.Posts); end < len(posts) {
		page.Next = nrEncodePosition(posts[end-1].ID)
	}
	return page, nil
}

func (s *demoRecordSource) Replies(ctx context.Context, r *http.Request, id string) (recordPage, error) {
	replies, err := s.Thread(ctx, r, id)
	if err != nil {
		return recordPage{}, err
	}
	page := recordPage{Posts: make([]post, 0, len(replies)), Coverage: demoCoverage}
	for _, item := range replies {
		page.Posts = append(page.Posts, post{ID: item.ID, Color: item.Color, Time: item.Time, Text: item.Text, Format: item.Format, Receipt: item.Receipt, IsReply: true, ReplyTo: id})
	}
	return page, nil
}

func (s *demoRecordSource) Successors(context.Context, *http.Request, string) (recordPage, error) {
	return recordPage{Coverage: demoCoverage}, nil
}

func (s *demoRecordSource) ByAuthor(ctx context.Context, r *http.Request, authorID string) (recordPage, error) {
	return s.matching(ctx, r, 0, func(p post) bool { return p.AuthorID == authorID || p.Handle == authorID })
}

func (s *demoRecordSource) Search(ctx context.Context, r *http.Request, query string, limit int) (recordPage, error) {
	needle := strings.ToLower(query)
	page, err := s.matching(ctx, r, nrLimit(limit), func(p post) bool { return needle != "" && strings.Contains(strings.ToLower(p.Text), needle) })
	page.Coverage.Note = "Searching demo data on this server"
	return page, err
}

func (s *demoRecordSource) matching(ctx context.Context, r *http.Request, limit int, match func(post) bool) (recordPage, error) {
	posts, err := s.Posts(ctx, r)
	if err != nil {
		return recordPage{}, err
	}
	page := recordPage{Coverage: demoCoverage}
	for _, p := range posts {
		if match(p) && (limit == 0 || len(page.Posts) < limit) {
			page.Posts = append(page.Posts, p)
		}
	}
	return page, nil
}

func (a *App) records() recordSource {
	return a.recordSource
}

func (a *App) requestPost(r *http.Request, id string) (post, bool) {
	p, ok, err := a.records().Post(r.Context(), r, id)
	if err != nil || !ok {
		return post{}, false
	}
	one := []post{p}
	a.forReader(r, one)
	return one[0], true
}
