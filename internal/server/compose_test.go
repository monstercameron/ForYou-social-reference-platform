package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

var (
	cmpTestNow       = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cmpTestMessageID = "fy1:" + strings.Repeat("a", 64)
	cmpTestParentID  = "fy1:" + strings.Repeat("b", 64)
	cmpTestSchedule  = "fyfs1:" + strings.Repeat("c", 64)
	cmpTestQuoteID   = "fyfq2:" + strings.Repeat("d", 64)
)

// cmpTestFlow replaces the publisher so that tests control every provider answer.
type cmpTestFlow struct {
	prepared   sdk.Prepared
	quoted     sdk.Quoted
	quoteErr   error
	publishErr error
	resumed    []string
	entries    map[string]sdk.JournalEntry
}

func newCmpTestFlow() *cmpTestFlow {
	return &cmpTestFlow{
		prepared: sdk.Prepared{MessageID: cmpTestMessageID, BillableBytes: 412, Fee: "136", Denomination: "msat", Network: wire.BitcoinMainnet, ScheduleID: cmpTestSchedule},
		quoted: sdk.Quoted{
			Prepared: sdk.Prepared{MessageID: cmpTestMessageID, BillableBytes: 412, Fee: "136", Denomination: "msat", Network: wire.BitcoinMainnet, ScheduleID: cmpTestSchedule},
			Quote:    wire.FeeQuote{MessageID: cmpTestMessageID, ScheduleID: cmpTestSchedule, QuoteID: cmpTestQuoteID, Network: wire.BitcoinMainnet, AmountBaseUnits: "136", ExpiresAt: wire.FormatTime(cmpTestNow.Add(10 * time.Minute))},
		},
		entries: map[string]sdk.JournalEntry{},
	}
}

func (f *cmpTestFlow) Prepare(sdk.Draft, ed25519.PrivateKey, string, *sdk.TargetEvidence) (sdk.Prepared, error) {
	return f.prepared, nil
}

func (f *cmpTestFlow) QuoteDraft(context.Context, sdk.Draft, ed25519.PrivateKey, string, *sdk.TargetEvidence) (sdk.Quoted, error) {
	if f.quoteErr != nil {
		return sdk.Quoted{}, f.quoteErr
	}
	f.entries[cmpTestMessageID] = sdk.JournalEntry{MessageID: cmpTestMessageID, State: "quoted", Prepared: f.quoted.Prepared, Quote: f.quoted.Quote}
	return f.quoted, nil
}

func (f *cmpTestFlow) Publish(_ context.Context, _ ed25519.PrivateKey, _ sdk.Quoted, _ sdk.Approval) (sdk.Published, error) {
	if f.publishErr != nil {
		return sdk.Published{}, f.publishErr
	}
	entry := f.entries[cmpTestMessageID]
	entry.State = "accepted"
	entry.Receipt = wire.SubmissionReceipt{ReceiptID: "rcpt-test", IssuedAt: "2026-11-11T12:00:00Z"}
	f.entries[cmpTestMessageID] = entry
	return sdk.Published{MessageID: cmpTestMessageID}, nil
}

func (f *cmpTestFlow) Resume(_ context.Context, _ ed25519.PrivateKey, id string) (sdk.JournalEntry, error) {
	f.resumed = append(f.resumed, id)
	return f.entries[id], nil
}

func (f *cmpTestFlow) Entry(id string) (sdk.JournalEntry, bool, error) {
	entry, ok := f.entries[id]
	return entry, ok, nil
}

func (f *cmpTestFlow) Entries() ([]sdk.JournalEntry, error) {
	entries := make([]sdk.JournalEntry, 0, len(f.entries))
	for _, entry := range f.entries {
		entries = append(entries, entry)
	}
	return entries, nil
}

// cmpTestState chooses which publishing preconditions the test profile meets.
type cmpTestState struct {
	identity, acknowledged, provider, ready bool
}

// cmpLoopbackResolver answers every host with the loopback address.
type cmpLoopbackResolver struct{}

func (cmpLoopbackResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// cmpTestSetup starts a dev client with a provider, a ready trust root and an acknowledged profile.
func cmpTestSetup(t *testing.T, flow *cmpTestFlow, identity bool) (http.Handler, *http.Cookie) {
	t.Helper()
	h, cookie, _ := cmpTestSetupApp(t, flow, identity)
	return h, cookie
}

func cmpTestSetupApp(t *testing.T, flow *cmpTestFlow, identity bool) (http.Handler, *http.Cookie, *App) {
	t.Helper()
	return cmpTestBuild(t, flow, cmpTestState{identity: identity, acknowledged: true, provider: true, ready: true})
}

func cmpTestBuild(t *testing.T, flow *cmpTestFlow, state cmpTestState) (http.Handler, *http.Cookie, *App) {
	t.Helper()
	var providers []Endpoint
	if state.provider {
		providers = []Endpoint{{URL: "http://127.0.0.1:8545", ServiceID: keys.DIDOf(devnet.ProviderKey(0)), Label: "Test provider"}}
	}
	client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true, Now: func() time.Time { return cmpTestNow }, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	profile := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if state.identity {
		if _, err := client.app.keystore().Create(profile); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.app.updateSettings(profile, func(s *ProfileSettings) error {
		if state.acknowledged {
			s.Acknowledgements = map[string]time.Time{ackKeyWarnings: cmpTestNow}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	client.app.trustMu.Lock()
	if state.ready {
		client.app.root = &sdk.TrustRoot{}
		client.app.trustStatus = TrustStatus{State: TrustReady, FetchedAt: cmpTestNow}
	} else {
		client.app.root = nil
		client.app.trustStatus = TrustStatus{State: TrustUnavailable}
	}
	client.app.trustMu.Unlock()
	previousFlow, previousBalance, previousRetention, previousTarget := cmpNewFlow, cmpBalance, cmpRetention, cmpFetchTarget
	cmpNewFlow = func(*sdk.Client, *sdk.TrustRoot, sdk.Journal, func() time.Time) cmpFlow { return flow }
	cmpBalance = func(context.Context, *sdk.Client, ed25519.PrivateKey, string) (wire.BalanceStatus, error) {
		return wire.BalanceStatus{Available: "1000000"}, nil
	}
	cmpRetention = func(context.Context, *sdk.Client) (wire.RetentionDeclarationsPage, error) {
		return wire.RetentionDeclarationsPage{}, errors.New("provider unavailable")
	}
	cmpFetchTarget = func(context.Context, *sdk.Client, *sdk.Verifier, string) (sdk.Record, error) {
		return sdk.Record{}, errCmpUnverified
	}
	t.Cleanup(func() {
		cmpNewFlow, cmpBalance, cmpRetention, cmpFetchTarget = previousFlow, previousBalance, previousRetention, previousTarget
	})
	return client.Handler(), &http.Cookie{Name: profileCookie, Value: profile}, client.app
}

// cmpTestRecord is a verified earlier post by the given author.
func cmpTestRecord(author, replyTo, text string) sdk.Record {
	payload := wire.PostPayload{ContentFormat: "text/plain", Text: text, ReplyTo: replyTo}
	return sdk.Record{ID: cmpTestMessageID, AuthorID: author, Message: wire.PostMessage{MessageID: cmpTestMessageID, AuthorID: author, Payload: payload}}
}

func cmpTestText(text string) url.Values {
	return url.Values{"format": {"text/plain"}, "text": {text}, "network": {wire.BitcoinMainnet}}
}

func cmpTestGet(h http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(cookie)
	h.ServeHTTP(w, r)
	return w
}

func TestComposePublishing(t *testing.T) {
	t.Run("without an identity the editor is replaced by the setup step", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), false)
		w := cmpTestGet(h, "/compose", cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Create an identity first.") || strings.Contains(body, `id="compose-form"`) {
			t.Errorf("status %d: without an identity the composer should show the setup step and no form", w.Code)
		}
	})
	t.Run("without the key warnings acknowledged the editor is replaced by the warnings step", func(t *testing.T) {
		h, cookie, _ := cmpTestBuild(t, newCmpTestFlow(), cmpTestState{identity: true, provider: true, ready: true})
		w := cmpTestGet(h, "/compose", cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Read the key warnings before your first post.") || strings.Contains(w.Body.String(), `id="compose-form"`) {
			t.Errorf("status %d: without the acknowledgement the composer should show the warnings step and no form", w.Code)
		}
	})
	t.Run("without a publishing provider the editor is replaced by the provider step", func(t *testing.T) {
		h, cookie, _ := cmpTestBuild(t, newCmpTestFlow(), cmpTestState{identity: true, acknowledged: true, ready: true})
		w := cmpTestGet(h, "/compose", cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Choose a publishing provider.") || strings.Contains(w.Body.String(), `id="compose-form"`) {
			t.Errorf("status %d: without a provider the composer should show the provider step and no form", w.Code)
		}
	})
	t.Run("without a trust root the editor is replaced by the paused step", func(t *testing.T) {
		h, cookie, _ := cmpTestBuild(t, newCmpTestFlow(), cmpTestState{identity: true, acknowledged: true, provider: true})
		w := cmpTestGet(h, "/compose", cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Publishing is paused.") || strings.Contains(w.Body.String(), `id="compose-form"`) {
			t.Errorf("status %d: without a trust root the composer should show the paused step and no form", w.Code)
		}
	})
	t.Run("measure shows the billable size and fee on both routes", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		fragment := postForm(h, "/compose/measure/fragment", cmpTestText("hello"), cookie)
		page := postForm(h, "/compose/check", cmpTestText("hello"), cookie)
		t.Run("fragment", func(t *testing.T) {
			if fragment.Code != http.StatusOK || !strings.Contains(fragment.Body.String(), "Billable size: 412 bytes") || !strings.Contains(fragment.Body.String(), "Fee: 136 msat") {
				t.Errorf("status %d; want the billable size 412 and fee 136 msat", fragment.Code)
			}
		})
		t.Run("full page fallback", func(t *testing.T) {
			if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Billable size: 412 bytes") || !strings.Contains(page.Body.String(), "Fee: 136 msat") {
				t.Errorf("status %d; want the billable size 412 and fee 136 msat", page.Code)
			}
		})
	})
	t.Run("a quote that differs from the schedule is never offered for confirmation", func(t *testing.T) {
		flow := newCmpTestFlow()
		flow.quoteErr = sdk.ErrQuoteMismatch
		h, cookie := cmpTestSetup(t, flow, true)
		w := postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
		body := w.Body.String()
		if w.Code != http.StatusConflict || !strings.Contains(body, "differs from the fee set by the schedule") || strings.Contains(body, "Confirm and post") {
			t.Errorf("status %d; a mismatched quote must not be confirmable", w.Code)
		}
	})
	t.Run("a quote whose balance is below the fee shows the shortfall and the wallet and no confirmation", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		previous := cmpBalance
		cmpBalance = func(context.Context, *sdk.Client, ed25519.PrivateKey, string) (wire.BalanceStatus, error) {
			return wire.BalanceStatus{Available: "100"}, nil
		}
		t.Cleanup(func() { cmpBalance = previous })
		w := postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Shortfall: 36 msat") || !strings.Contains(body, `href="/wallet"`) || strings.Contains(body, "Confirm and post") {
			t.Errorf("status %d; a balance of 100 msat under a 136 msat fee must show the shortfall and no confirmation", w.Code)
		}
	})
	t.Run("a quote whose balance cannot be read keeps the confirmation", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		previous := cmpBalance
		cmpBalance = func(context.Context, *sdk.Client, ed25519.PrivateKey, string) (wire.BalanceStatus, error) {
			return wire.BalanceStatus{}, errors.New("unreachable")
		}
		t.Cleanup(func() { cmpBalance = previous })
		w := postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Balance not available from the provider right now.") || !strings.Contains(body, "Confirm and post") {
			t.Errorf("status %d; an unreadable balance must say so and keep the confirmation", w.Code)
		}
	})
	t.Run("a missing permanence tick is named by its checkbox", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
		w := postForm(h, "/compose/publish/fragment", url.Values{"message_id": {cmpTestMessageID}, "approve_amount": {"on"}}, cookie)
		body := w.Body.String()
		if w.Code != http.StatusBadRequest || !strings.Contains(body, `aria-describedby="compose-permanence-error"`) || !strings.Contains(body, `id="compose-permanence-error" class="notice-bad">Tick the permanence notice to publish.`) || strings.Contains(body, ">Published<") {
			t.Errorf("status %d; the permanence checkbox must point at its error and nothing may publish", w.Code)
		}
	})
	t.Run("a missing amount tick is named by its checkbox and the approval names the provider", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
		w := postForm(h, "/compose/publish/fragment", url.Values{"message_id": {cmpTestMessageID}, "permanence": {"on"}}, cookie)
		body := w.Body.String()
		if w.Code != http.StatusBadRequest || !strings.Contains(body, `aria-describedby="compose-approve-error"`) || !strings.Contains(body, `id="compose-approve-error"`) || !strings.Contains(body, "from my balance at Test provider.") {
			t.Errorf("status %d; the approval checkbox must point at its error and name the provider", w.Code)
		}
	})
	t.Run("a confirmed post is shown as published with its message ID", func(t *testing.T) {
		flow := newCmpTestFlow()
		h, cookie := cmpTestSetup(t, flow, true)
		postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
		w := postForm(h, "/compose/publish/fragment", url.Values{"message_id": {cmpTestMessageID}, "permanence": {"on"}, "approve_amount": {"on"}}, cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "<h2 id=\"compose-quote-heading\" tabindex=\"-1\" data-focus-target>Published</h2>") || !strings.Contains(body, `href="/posts/`+cmpTestMessageID+`"`) || !strings.Contains(body, "rcpt-test") {
			t.Errorf("status %d; a confirmed post should show Published with its message ID and receipt", w.Code)
		}
	})
	t.Run("insufficient balance names the provider, links to the wallet and offers no retry", func(t *testing.T) {
		flow := newCmpTestFlow()
		flow.publishErr = &sdk.Error{HTTPStatus: http.StatusPaymentRequired, Status: "insufficient_balance", Endpoint: "v1/admissions"}
		h, cookie := cmpTestSetup(t, flow, true)
		postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
		w := postForm(h, "/compose/publish/fragment", url.Values{"message_id": {cmpTestMessageID}, "permanence": {"on"}, "approve_amount": {"on"}}, cookie)
		body := w.Body.String()
		if !strings.Contains(body, "Your balance at Test provider is too low") || !strings.Contains(body, `href="/wallet"`) || strings.Contains(body, "Retry the same request") {
			t.Errorf("status %d; an insufficient balance should name the provider, link to the wallet and offer no retry", w.Code)
		}
	})
	t.Run("an unknown outcome retries only the same stored request", func(t *testing.T) {
		flow := newCmpTestFlow()
		flow.publishErr = sdk.ErrOutcomeUnknown
		h, cookie := cmpTestSetup(t, flow, true)
		postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
		failed := postForm(h, "/compose/publish/fragment", url.Values{"message_id": {cmpTestMessageID}, "permanence": {"on"}, "approve_amount": {"on"}}, cookie)
		if !strings.Contains(failed.Body.String(), "Retry the same request") || !strings.Contains(failed.Body.String(), `name="message_id" value="`+cmpTestMessageID+`"`) {
			t.Fatalf("status %d; an unknown outcome should offer the retry of the stored request", failed.Code)
		}
		retried := postForm(h, "/compose/resume/fragment", url.Values{"message_id": {cmpTestMessageID}}, cookie)
		if retried.Code != http.StatusConflict || len(flow.resumed) != 1 || flow.resumed[0] != cmpTestMessageID {
			t.Errorf("status %d; resume should call Resume once with the same message ID, got %v", retried.Code, flow.resumed)
		}
	})
	t.Run("the unfinished list shows an authorized publication with its deadline", func(t *testing.T) {
		h, cookie, app := cmpTestSetupApp(t, newCmpTestFlow(), true)
		journal, err := app.cmpJournal(cookie.Value)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.Put(sdk.JournalEntry{MessageID: cmpTestMessageID, State: "authorized", Deadline: "2026-12-11T12:00:00Z", CreatedAt: "2026-11-11T12:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		w := cmpTestGet(h, "/compose", cookie)
		if !strings.Contains(w.Body.String(), cmpTestMessageID) || !strings.Contains(w.Body.String(), "finish before 2026-12-11T12:00:00Z") {
			t.Errorf("status %d; the unfinished list should show the authorized publication and its deadline", w.Code)
		}
	})
	t.Run("a reply to a parent the sources lack needs the explicit tick", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		form := cmpTestText("hello")
		form.Set("reply_to", cmpTestParentID)
		w := postForm(h, "/compose/quote/fragment", form, cookie)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Reply to this exact ID anyway") {
			t.Errorf("status %d; a missing parent must be refused until the author accepts it", w.Code)
		}
	})
	t.Run("a reply to a parent the sources lack is quoted once the author accepts it", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		form := cmpTestText("hello")
		form.Set("reply_to", cmpTestParentID)
		form.Set("allow_missing_parent", "on")
		w := postForm(h, "/compose/quote/fragment", form, cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Review and confirm") {
			t.Errorf("status %d; a missing parent accepted by the author should be quoted", w.Code)
		}
	})
	t.Run("a new version of another author's post is refused before any editor", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		cmpFetchTarget = func(context.Context, *sdk.Client, *sdk.Verifier, string) (sdk.Record, error) {
			return cmpTestRecord(keys.DIDOf(devnet.ProviderKey(0)), "", "someone else's text"), nil
		}
		w := cmpTestGet(h, "/compose?supersedes="+cmpTestMessageID, cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "You can only publish a new version of your own post") || strings.Contains(w.Body.String(), `id="compose-form"`) {
			t.Errorf("status %d; another author's post must not open an editor", w.Code)
		}
	})
	t.Run("a new version of one's own post keeps its text and reply target", func(t *testing.T) {
		h, cookie, app := cmpTestSetupApp(t, newCmpTestFlow(), true)
		key, err := app.keystore().PrivateKey(cookie.Value)
		if err != nil {
			t.Fatal(err)
		}
		cmpFetchTarget = func(context.Context, *sdk.Client, *sdk.Verifier, string) (sdk.Record, error) {
			return cmpTestRecord(keys.DIDOf(key), cmpTestParentID, "old text"), nil
		}
		w := cmpTestGet(h, "/compose?supersedes="+cmpTestMessageID, cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `name="supersedes" value="`+cmpTestMessageID+`"`) || !strings.Contains(body, `name="reply_to" value="`+cmpTestParentID+`"`) || !strings.Contains(body, "A new version keeps the same reply target") || !strings.Contains(body, ">old text</textarea>") {
			t.Errorf("status %d; a new version should carry the earlier text, its reply target and the supersedes ID", w.Code)
		}
	})
	t.Run("markdown links to undeclared media and references are named as inert", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		form := url.Values{"format": {"text/markdown"}, "text": {"![cat](fy-media:cat) and [Ann](fy-ref:ann)"}, "network": {wire.BitcoinMainnet}}
		w := postForm(h, "/compose/measure/fragment", form, cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "fy-media:cat has no media slot") || !strings.Contains(body, "fy-ref:ann has no declared reference") {
			t.Errorf("status %d; undeclared fy-media and fy-ref links should be named", w.Code)
		}
	})
	t.Run("a media link to a loopback address is reported as blocked", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		previous := cmpMediaPolicy
		cmpMediaPolicy = content.DestinationPolicy{Resolver: cmpLoopbackResolver{}}
		t.Cleanup(func() { cmpMediaPolicy = previous })
		form := cmpTestText("hello")
		form.Set("media_slot", "pic")
		form.Set("media_kind", "image")
		form.Set("media_url", "https://media.example/a.png")
		form.Set("media_sha256", strings.Repeat("0", 64))
		form.Set("media_size", "")
		form.Set("media_alt", "")
		form.Set("row_action", "digest-media:0")
		w := postForm(h, "/compose/rows/fragment", form, cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Blocked: private or loopback address") {
			t.Errorf("status %d; a loopback media link must be reported as blocked", w.Code)
		}
	})
	t.Run("media rows must use HTTPS and keep the text when a row is removed", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		insecure := cmpTestText("hello")
		insecure.Set("media_slot", "pic")
		insecure.Set("media_kind", "image")
		insecure.Set("media_url", "http://media.example/a.png")
		insecure.Set("media_sha256", strings.Repeat("0", 64))
		insecure.Set("media_size", "")
		insecure.Set("media_alt", "")
		if w := postForm(h, "/compose/quote/fragment", insecure, cookie); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Media links must use HTTPS.") {
			t.Errorf("status %d; an http media link must be reported invalid", w.Code)
		}
		rows := cmpTestText("keep me")
		rows["media_slot"] = []string{"a", "b"}
		rows["media_kind"] = []string{"image", "image"}
		rows["media_url"] = []string{"https://media.example/a.png", "https://media.example/b.png"}
		rows["media_sha256"] = []string{strings.Repeat("0", 64), strings.Repeat("1", 64)}
		rows["media_size"] = []string{"", ""}
		rows["media_alt"] = []string{"", ""}
		rows.Set("row_action", "remove-media:0")
		w := postForm(h, "/compose/rows/fragment", rows, cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || strings.Contains(body, `value="a"`) || !strings.Contains(body, `value="b"`) || !strings.Contains(body, "keep me") {
			t.Errorf("status %d; removing a row should keep the other rows and the text", w.Code)
		}
	})
	t.Run("a media row added and then removed leaves the text and no row", func(t *testing.T) {
		h, cookie := cmpTestSetup(t, newCmpTestFlow(), true)
		add := cmpTestText("keep me")
		add.Set("row_action", "add-media")
		added := postForm(h, "/compose/rows/fragment", add, cookie)
		if added.Code != http.StatusOK || !strings.Contains(added.Body.String(), `value="remove-media:0"`) {
			t.Fatalf("status %d; adding a media row should show a row to remove", added.Code)
		}
		remove := cmpTestText("keep me")
		remove.Set("media_slot", "")
		remove.Set("media_kind", "image")
		remove.Set("media_url", "")
		remove.Set("media_sha256", "")
		remove.Set("media_size", "")
		remove.Set("media_alt", "")
		remove.Set("row_action", "remove-media:0")
		removed := postForm(h, "/compose/rows/fragment", remove, cookie)
		body := removed.Body.String()
		if removed.Code != http.StatusOK || strings.Contains(body, `name="media_slot"`) || !strings.Contains(body, "keep me") {
			t.Errorf("status %d; removing the added row should leave no media row and keep the text", removed.Code)
		}
	})
}

// cmpFakeProvider answers fee quotes, admissions and final messages as the protocol describes, so that the real publisher runs end to end.
type cmpFakeProvider struct {
	t        *testing.T
	root     *sdk.TrustRoot
	key      ed25519.PrivateKey
	auth     *delegation.ServiceAuth
	mu       sync.Mutex
	quotes   map[string]wire.FeeQuote
	accepted []string
}

func (f *cmpFakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/v1/fee-quotes":
		f.quote(w, body)
	case r.URL.Path == "/v1/admissions":
		f.admit(w, body)
	case r.URL.Path == "/v1/messages":
		f.finalize(w, body)
	case r.URL.Path == "/v1/service-challenges":
		f.challenge(w, body)
	case strings.HasPrefix(r.URL.Path, "/v1/balances/"):
		f.balance(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *cmpFakeProvider) challenge(w http.ResponseWriter, body []byte) {
	request, err := delegation.ParseChallengeRequest(body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	challenge, err := f.auth.Issue("test", request)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.write(w, http.StatusOK, challenge)
}

func (f *cmpFakeProvider) balance(w http.ResponseWriter, r *http.Request) {
	author, err := f.auth.Verify(delegation.NewAuthRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get, delegation.BodyNone, nil))
	if err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.Unauthenticated})
		return
	}
	status := wire.BalanceStatus{ProviderID: keys.DIDOf(f.key), AuthorID: author, Network: strings.TrimPrefix(r.URL.Path, "/v1/balances/"), Denomination: "millisatoshi", Available: "10000", Frozen: "0", Reserved: "0", Revision: 1, UpdatedAt: wire.FormatTime(cmpTestNow)}
	encoded, err := canon.EncodeWithout(status, "provider_signature")
	if err == nil {
		status.ProviderSignature, err = keys.Sign(f.key, "balance-status", encoded)
	}
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.write(w, http.StatusOK, status)
}

func (f *cmpFakeProvider) quote(w http.ResponseWriter, body []byte) {
	var request wire.FeePreflightRequest
	var intent wire.Intent
	if wire.Decode(body, "FeePreflightRequest", &request) != nil || wire.Decode(request.Intent, "AdmissionRequest/properties/intent", &intent) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id, err := message.ID(intent)
	if err != nil || fee.VerifyPreflight(fee.PreflightAuthorization(request.ProviderID, request.AuthorID, id, request.ScheduleID), request.AuthorID, request.Authorization) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	canonical, err := canon.Encode(intent)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	schedule, _ := f.root.ScheduleInForce(wire.BitcoinMainnet, cmpTestNow)
	quote, err := fee.IssueQuote(fee.QuoteRequest{ProviderID: request.ProviderID, AuthorID: request.AuthorID, MessageID: id, BillableBytes: len(canonical), PreflightAt: cmpTestNow, ExpiresAt: cmpTestNow.Add(10 * time.Minute)}, schedule, []wire.FeeSchedule{schedule}, f.key)
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	f.quotes[quote.QuoteID] = quote
	f.mu.Unlock()
	f.write(w, http.StatusOK, quote)
}

func (f *cmpFakeProvider) admit(w http.ResponseWriter, body []byte) {
	var request wire.AdmissionRequest
	if wire.Decode(body, "AdmissionRequest", &request) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id, err := message.ID(request.Intent)
	if err != nil || fee.VerifyAdmission(fee.AdmissionAuthorization(request.ProviderID, request.Intent.AuthorID, id, request.QuoteID), request.Intent.AuthorID, request.Authorization) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	quote, ok := f.quotes[request.QuoteID]
	f.mu.Unlock()
	if !ok || quote.MessageID != id {
		apierr.Write(w, apierr.PublicReadError{Status: "quote_conflict"})
		return
	}
	receipt, err := fee.SignReceipt(wire.SubmissionReceipt{ProviderID: quote.ProviderID, AuthorID: quote.AuthorID, MessageID: id, ReceiptID: keys.EncodeB64URL(make([]byte, 32)), QuoteID: quote.QuoteID, ScheduleID: quote.ScheduleID, Network: quote.Network, AmountBaseUnits: quote.AmountBaseUnits, AuthorizationAt: wire.FormatTime(cmpTestNow), IssuedAt: wire.FormatTime(cmpTestNow)}, f.key)
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.write(w, http.StatusCreated, receipt)
}

func (f *cmpFakeProvider) finalize(w http.ResponseWriter, body []byte) {
	var record wire.PostMessage
	if wire.Decode(body, "PostMessage", &record) != nil || message.Verify(record, message.VerifyOptions{Now: cmpTestNow}) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.accepted = append(f.accepted, record.MessageID)
	f.mu.Unlock()
	f.write(w, http.StatusCreated, wire.MessageAcceptance{MessageID: record.MessageID, Status: "accepted"})
}

func (f *cmpFakeProvider) write(w http.ResponseWriter, status int, value any) {
	data, err := canon.Encode(value)
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func TestComposePublishesThroughThePublisher(t *testing.T) {
	genesis := devnet.Genesis()
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{Genesis: genesis, FetchedAt: cmpTestNow})
	if err != nil {
		t.Fatal(err)
	}
	providerKey := devnet.ProviderKey(0)
	auth, err := delegation.NewServiceAuth(delegation.ServiceAuthConfig{ServiceID: keys.DIDOf(providerKey), Store: delegation.NewMemoryChallengeStore(), Now: func() time.Time { return cmpTestNow }, Limits: delegation.Limits{MaxOutstanding: 64, MaxPerSource: 64}})
	if err != nil {
		t.Fatal(err)
	}
	fake := &cmpFakeProvider{t: t, root: root, key: ed25519.PrivateKey(providerKey), auth: auth, quotes: map[string]wire.FeeQuote{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true, Now: func() time.Time { return cmpTestNow }, Providers: []Endpoint{{URL: server.URL, ServiceID: keys.DIDOf(providerKey), Label: "Test provider"}}})
	if err != nil {
		t.Fatal(err)
	}
	profile := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if _, err := client.app.keystore().Create(profile); err != nil {
		t.Fatal(err)
	}
	if err := client.app.updateSettings(profile, func(s *ProfileSettings) error {
		s.Acknowledgements = map[string]time.Time{ackKeyWarnings: cmpTestNow}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	client.app.trustMu.Lock()
	client.app.root = root
	client.app.trustStatus = TrustStatus{State: TrustReady, FetchedAt: cmpTestNow}
	client.app.trustMu.Unlock()
	h, cookie := client.Handler(), &http.Cookie{Name: profileCookie, Value: profile}

	quoted := postForm(h, "/compose/quote/fragment", cmpTestText("hello"), cookie)
	match := regexp.MustCompile(`name="message_id" value="(fy1:[0-9a-z]+)"`).FindStringSubmatch(quoted.Body.String())
	if quoted.Code != http.StatusOK || match == nil || !strings.Contains(quoted.Body.String(), "136 msat") || !strings.Contains(quoted.Body.String(), "Matches your own calculation") {
		t.Fatalf("status %d; the provider's signed quote for hello should be 136 msat and confirmable", quoted.Code)
	}
	id := match[1]
	published := postForm(h, "/compose/publish/fragment", url.Values{"message_id": {id}, "permanence": {"on"}, "approve_amount": {"on"}}, cookie)
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), ">Published</h2>") || !strings.Contains(published.Body.String(), `href="/posts/`+id+`"`) {
		t.Fatalf("status %d; the confirmed post should be published with its message ID", published.Code)
	}
	t.Run("the provider received the finalized message once", func(t *testing.T) {
		if len(fake.accepted) != 1 || fake.accepted[0] != id {
			t.Errorf("provider accepted %v; want only %s", fake.accepted, id)
		}
	})
	t.Run("the published record is held in the local copy as the author's own", func(t *testing.T) {
		source, ok := client.app.networkSource()
		if !ok {
			t.Fatal("the client should read through the network source")
		}
		entry, found := source.store.get(id)
		if !found || !entry.Mine {
			t.Errorf("record held %t, mine %t; want the published post held as the author's own", found, entry.Mine)
		}
	})
}
