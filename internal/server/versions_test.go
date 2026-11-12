package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

const (
	verAuthorA = "did:key:z6MkVersionAuthorA"
	verAuthorB = "did:key:z6MkVersionAuthorB"
)

var verStart = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// verFakeSource serves fixed verified records; the page code reads only Get and Successors.
type verFakeSource struct {
	recordSource
	posts      map[string]post
	successors map[string][]post
	later      Coverage
}

func (f verFakeSource) Get(_ context.Context, _ *http.Request, id string) (post, Coverage, bool, error) {
	p, ok := f.posts[id]
	return p, Coverage{Note: "Records held by the test source"}, ok, nil
}

func (f verFakeSource) Successors(_ context.Context, _ *http.Request, id string) (recordPage, error) {
	return recordPage{Posts: f.successors[id], Coverage: f.later}, nil
}

const (
	verFixtureRoot  = "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	verFixtureLeaf  = "fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b"
	verFixtureHead  = "fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf"
	verFixtureReply = "fy1:e703e832f979cb8c1dc232ad719a4f09117617c7852774121e90b4ca2e345262"
)

func verVersion(id, author, supersedes string, issuedAt time.Time) post {
	return post{ID: id, AuthorID: author, Supersedes: supersedes, Text: "Text of " + id, Class: "locally_admitted", IssuedAt: issuedAt, RootSigned: true, Timestamp: wire.FormatTime(issuedAt)}
}

func verFixtureMessages(t *testing.T) []wire.PostMessage {
	t.Helper()
	data, err := os.ReadFile("../sdk/testdata/as/post-version-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Messages []wire.PostMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f.Messages
}

func verFromMessage(t *testing.T, m wire.PostMessage) post {
	t.Helper()
	issuedAt, err := wire.ParseTime(m.SubmissionReceipt.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	return post{ID: m.MessageID, AuthorID: m.AuthorID, Text: m.Payload.Text, ReplyTo: m.Payload.ReplyTo, Supersedes: m.Payload.Supersedes, Class: "locally_admitted", IssuedAt: issuedAt, RootSigned: message.RootSigned(m), Timestamp: m.Timestamp}
}

func verHandlerWith(t *testing.T, posts ...post) http.Handler {
	t.Helper()
	client, err := NewClient(Config{DataDir: t.TempDir(), PublicOrigin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	source := verFakeSource{posts: make(map[string]post), successors: make(map[string][]post)}
	for _, p := range posts {
		source.posts[p.ID] = p
		if p.Supersedes != "" {
			source.successors[p.Supersedes] = append(source.successors[p.Supersedes], p)
		}
	}
	client.app.recordSource = source
	return client.Handler()
}

func verGet(t *testing.T, h http.Handler, path string, status int, cookies ...*http.Cookie) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("GET %s: status %d; want %d", path, w.Code, status)
	}
	return w.Body.String()
}

func verExpect(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("page does not contain %q", want)
	}
}

func verAbsent(t *testing.T, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Errorf("page contains %q; it must not", unwanted)
	}
}

func TestVersionHistory(t *testing.T) {
	t.Run("fork selects the latest receipt and keeps every branch", func(t *testing.T) {
		h := verHandlerWith(t,
			verVersion("v1", verAuthorA, "", verStart),
			verVersion("v2a", verAuthorA, "v1", verStart.Add(time.Hour)),
			verVersion("v2b", verAuthorA, "v1", verStart.Add(2*time.Hour)),
		)
		body := verGet(t, h, "/posts/v2a/versions", http.StatusOK)
		verExpect(t, body, "This post has 2 competing latest versions. All are shown below.")
		verExpect(t, body, "Text of v2b")
		verExpect(t, body, "Latest provider receipt time among the known versions")
		verExpect(t, body, "<li class=\"version-node is-selected\" aria-current=\"true\">\n  <div class=\"version-row\">\n    <a class=\"id-chip version-id\" href=\"/posts/v2b\">v2b</a>")
		verAbsent(t, body, "<li class=\"version-node is-selected\" aria-current=\"true\">\n  <div class=\"version-row\">\n    <a class=\"id-chip version-id\" href=\"/posts/v2a\">")
		verExpect(t, body, `href="/posts/v2a">v2a</a>`)
		verExpect(t, body, `aria-label="Branch from v1"`)
	})

	t.Run("fixture fork shows the latest receipt and keeps the reply out of the versions", func(t *testing.T) {
		messages := verFixtureMessages(t)
		h := verHandlerWith(t,
			verFromMessage(t, messages[0]),
			verFromMessage(t, messages[1]),
			verFromMessage(t, messages[2]),
			verFromMessage(t, messages[3]),
			verFromMessage(t, messages[4]),
		)
		body := verGet(t, h, "/posts/"+verFixtureRoot+"/versions", http.StatusOK)
		verExpect(t, body, "This post has 2 competing latest versions. All are shown below.")
		verExpect(t, body, "Latest provider receipt time among the known versions")
		verExpect(t, body, "<li class=\"version-node is-selected\" aria-current=\"true\">\n  <div class=\"version-row\">\n    <a class=\"id-chip version-id\" href=\"/posts/"+verFixtureHead+"\">fy1:569bfd9a…</a>")
		verExpect(t, body, "href=\"/posts/"+verFixtureLeaf+"\">fy1:c33c9c7d…</a>")
		verAbsent(t, body, "A reply to the first version.")
		verAbsent(t, body, "/posts/"+verFixtureReply)
	})

	t.Run("cut-off predecessor walk still shows the successor and says so", func(t *testing.T) {
		var posts []post
		for i := 0; i <= 70; i++ {
			supersedes := ""
			if i > 0 {
				supersedes = fmt.Sprintf("c%02d", i-1)
			}
			posts = append(posts, verVersion(fmt.Sprintf("c%02d", i), verAuthorA, supersedes, verStart.Add(time.Duration(i)*time.Minute)))
		}
		posts = append(posts, verVersion("c71", verAuthorA, "c70", verStart.Add(72*time.Hour)))
		body := verGet(t, verHandlerWith(t, posts...), "/posts/c70/versions", http.StatusOK)
		verExpect(t, body, "<li class=\"version-node is-selected\" aria-current=\"true\">\n  <div class=\"version-row\">\n    <a class=\"id-chip version-id\" href=\"/posts/c71\">c71</a>")
		verExpect(t, body, "Shown by default</span> Only known latest version")
		verExpect(t, body, "History is longer than this client follows; showing the first")
		verAbsent(t, body, "Earlier version not available from your sources")
	})

	t.Run("failed lookup for later versions is reported", func(t *testing.T) {
		client, err := NewClient(Config{DataDir: t.TempDir(), PublicOrigin: "https://example.test"})
		if err != nil {
			t.Fatal(err)
		}
		client.app.recordSource = verFakeSource{
			posts:      map[string]post{"v1": verVersion("v1", verAuthorA, "", verStart)},
			successors: map[string][]post{},
			later:      Coverage{Sources: []SourceStatus{{Label: "Provider one", OK: false, Text: "did not answer"}, {Label: "Local copy", OK: true, Text: "0 more held records match"}}},
		}
		body := verGet(t, client.Handler(), "/posts/v1/versions", http.StatusOK)
		verExpect(t, body, "<li>Later versions: Provider one: did not answer</li>")
		verAbsent(t, body, "0 more held records match")
	})

	t.Run("unavailable earlier version is named and the only version is the default", func(t *testing.T) {
		h := verHandlerWith(t, verVersion("v2", verAuthorA, "v1", verStart.Add(time.Hour)))
		body := verGet(t, h, "/posts/v2/versions", http.StatusOK)
		verExpect(t, body, `Earlier version not available from your sources (<span class="id-chip">v1</span>)`)
		verExpect(t, body, "Only known latest version")
		verAbsent(t, body, "competing latest versions")
	})

	t.Run("rejected link stays listed beside the chain", func(t *testing.T) {
		h := verHandlerWith(t,
			verVersion("v1", verAuthorA, "", verStart),
			verVersion("v2", verAuthorB, "v1", verStart.Add(time.Hour)),
		)
		body := verGet(t, h, "/posts/v1/versions", http.StatusOK)
		verExpect(t, body, "Not a valid version link: different author")
		verExpect(t, body, `href="/posts/v2">v2</a>`)
		verAbsent(t, body, "competing latest versions")
	})

	t.Run("demo post has no verified chain", func(t *testing.T) {
		body := verGet(t, testClientHandler(t), "/posts/maya/versions", http.StatusOK)
		verExpect(t, body, "This post is not a verified network record, so its version history cannot be checked.")
	})

	t.Run("new version link only for the author", func(t *testing.T) {
		client, err := NewClient(Config{DataDir: t.TempDir(), PublicOrigin: "https://example.test"})
		if err != nil {
			t.Fatal(err)
		}
		identity, err := client.app.keystore().Create(setTestProfile)
		if err != nil {
			t.Fatal(err)
		}
		source := verFakeSource{posts: map[string]post{"v1": verVersion("v1", identity.DID, "", verStart)}, successors: map[string][]post{}}
		client.app.recordSource = source
		h := client.Handler()
		cookie := &http.Cookie{Name: profileCookie, Value: setTestProfile}
		verExpect(t, verGet(t, h, "/posts/v1/versions", http.StatusOK, cookie), `href="/compose?supersedes=v1"`)
		verAbsent(t, verGet(t, h, "/posts/v1/versions", http.StatusOK), "/compose?supersedes=")
	})

	t.Run("fragment is part of the full page", func(t *testing.T) {
		h := verHandlerWith(t,
			verVersion("v1", verAuthorA, "", verStart),
			verVersion("v2a", verAuthorA, "v1", verStart.Add(time.Hour)),
			verVersion("v2b", verAuthorA, "v1", verStart.Add(2*time.Hour)),
		)
		page := verGet(t, h, "/posts/v2a/versions", http.StatusOK)
		fragment := verGet(t, h, "/posts/v2a/versions/fragment", http.StatusOK)
		verExpect(t, fragment, "This post has 2 competing latest versions.")
		verAbsent(t, fragment, "Version history</h1>")
		verExpect(t, page, fragment)
	})

	t.Run("unknown post is not found", func(t *testing.T) {
		verGet(t, verHandlerWith(t), "/posts/unknown/versions", http.StatusNotFound)
	})
}
