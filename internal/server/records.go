package server

import (
	"context"
	"net/http"
	"time"
)

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
