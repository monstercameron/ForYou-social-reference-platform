package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/wire"
)

// replyQuoteLifetime is how long a reader has to confirm a quoted reply.
const replyQuoteLifetime = 10 * time.Minute

const demoQuoteLimit = 2000

type networkFee struct{ Asset, Amount, Unit string }

// postCost is what a post or reply of some size costs under the fee schedule
// in force. The demo counts the text alone; a provider's signed quote counts
// the whole signed post.
type postCost struct {
	Bytes, Units int
	Fees         []networkFee
}

var feeLabels = map[string]networkFee{
	wire.BitcoinMainnet:  {Asset: "Bitcoin", Unit: "msat"},
	wire.EthereumMainnet: {Asset: "Ethereum", Unit: "wei"},
}

var demoFeeSchedules = devnet.FeeSchedules()

func groupDigits(digits string) string {
	var grouped strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return grouped.String()
}

func costOf(text string) (postCost, error) {
	cost := postCost{Bytes: len(text), Units: (len(text) + 1023) / 1024}
	for _, approved := range demoFeeSchedules {
		amount, err := fee.Amount(approved.Schedule, cost.Bytes)
		if err != nil {
			return postCost{}, err
		}
		label := feeLabels[approved.Schedule.Network]
		label.Amount = groupDigits(fee.FormatAmount(amount))
		cost.Fees = append(cost.Fees, label)
	}
	return cost, nil
}

// replyDraft is a reply on its way to being posted: first written, then quoted
// and shown for confirmation.
type replyDraft struct {
	PostID, Path, Text string
	Cost               postCost
	Quote              string
	Standalone         bool
}

// DraftID names the element that holds the draft of one reply.
func (d replyDraft) DraftID() string {
	return "draft-" + d.PostID + "-" + strings.ReplaceAll(d.Path, ".", "-")
}

type replyConfirmPage struct {
	replyDraft
	Theme, Section, Filter, Query string
}

type quotedReply struct {
	draft   replyDraft
	expires time.Time
}

// quoteReply keeps a quoted draft until the reader confirms it or it expires.
func (s *demoStore) quoteReply(d replyDraft, now time.Time) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, quoted := range s.quotes {
		if !now.Before(quoted.expires) {
			delete(s.quotes, token)
		}
	}
	if len(s.quotes) >= demoQuoteLimit {
		return "", errDemoStoreFull
	}
	if s.quotes == nil {
		s.quotes = make(map[string]quotedReply)
	}
	token := hex.EncodeToString(id[:])
	s.quotes[token] = quotedReply{draft: d, expires: now.Add(replyQuoteLifetime)}
	return token, nil
}

// takeQuote returns a quoted draft once. A second confirmation of the same
// quote finds nothing, so one quote never posts twice.
func (s *demoStore) takeQuote(token string, now time.Time) (replyDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	quoted, ok := s.quotes[token]
	delete(s.quotes, token)
	if !ok || !now.Before(quoted.expires) {
		return replyDraft{}, false
	}
	return quoted.draft, true
}

// readReplyDraft reads and checks a written reply. It answers the client itself when the draft is refused.
func readReplyDraft(w http.ResponseWriter, r *http.Request) (replyDraft, bool) {
	form, ok := readActionForm(w, r, "id", "parent", "text")
	if !ok {
		return replyDraft{}, false
	}
	d := replyDraft{PostID: form.Get("id"), Path: form.Get("parent"), Text: strings.TrimSpace(form.Get("text"))}
	if _, found := requestPost(r, d.PostID); !found {
		http.NotFound(w, r)
		return replyDraft{}, false
	}
	if d.Text == "" || !utf8.ValidString(d.Text) || utf8.RuneCountInString(d.Text) > replyRuneLimit {
		http.Error(w, "Write a reply of up to 1,000 characters.", http.StatusBadRequest)
		return replyDraft{}, false
	}
	if d.Path != "" {
		if chain := replyChain(threadReplies(r, d.PostID), d.Path); chain == nil || !chain[len(chain)-1].Open {
			http.Error(w, "This reply cannot be answered.", http.StatusBadRequest)
			return replyDraft{}, false
		}
	}
	return d, true
}

// replyQuoteHandler prices a written reply and asks the reader to confirm it.
// A reply is a post: nothing is posted until its cost has been shown and accepted.
func replyQuoteHandler(w http.ResponseWriter, r *http.Request) {
	d, ok := readReplyDraft(w, r)
	if !ok {
		return
	}
	cost, err := costOf(d.Text)
	store := storeFor(r)
	if err != nil || store == nil {
		http.Error(w, "No fee quote is available.", http.StatusServiceUnavailable)
		return
	}
	d.Cost = cost
	if d.Quote, err = store.quoteReply(d, time.Now()); err != nil {
		http.Error(w, "Demo storage is full or unavailable.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/reply/quote/fragment" {
		_ = home.ExecuteTemplate(w, "reply-confirm", d)
		return
	}
	d.Standalone = true
	_ = home.ExecuteTemplate(w, "reply-confirm-page", replyConfirmPage{replyDraft: d, Theme: requestTheme(r), Filter: "all"})
}

// replyEditHandler puts a quoted reply back into its form so the reader can change it.
func replyEditHandler(w http.ResponseWriter, r *http.Request) {
	d, ok := readReplyDraft(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = home.ExecuteTemplate(w, "reply-write", d)
}
