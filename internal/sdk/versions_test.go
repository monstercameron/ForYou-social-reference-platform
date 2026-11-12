package sdk

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"foryou.local/foryou/internal/wire"
)

func asVersionFixture(t *testing.T) ([]wire.PostMessage, map[string]string) {
	t.Helper()
	data, err := os.ReadFile("testdata/as/post-version-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Messages []wire.PostMessage `json:"messages"`
		Expected map[string]string  `json:"expected"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f.Messages, f.Expected
}

func TestBuildVersionGraph(t *testing.T) {
	t.Run("fixture fork selects latest receipt", func(t *testing.T) {
		messages, expected := asVersionFixture(t)
		a, err := wire.ParseTime(messages[0].SubmissionReceipt.IssuedAt)
		if err != nil {
			t.Fatal(err)
		}
		b, err := wire.ParseTime(messages[1].SubmissionReceipt.IssuedAt)
		if err != nil {
			t.Fatal(err)
		}
		c, err := wire.ParseTime(messages[2].SubmissionReceipt.IssuedAt)
		if err != nil {
			t.Fatal(err)
		}
		d, err := wire.ParseTime(messages[3].SubmissionReceipt.IssuedAt)
		if err != nil {
			t.Fatal(err)
		}
		e, err := wire.ParseTime(messages[4].SubmissionReceipt.IssuedAt)
		if err != nil {
			t.Fatal(err)
		}
		inputs := []VersionInput{{Message: messages[0], IssuedAt: a}, {Message: messages[1], IssuedAt: b}, {Message: messages[2], IssuedAt: c}, {Message: messages[3], IssuedAt: d}, {Message: messages[4], IssuedAt: e}}
		got := BuildVersionGraph(messages[0].MessageID, inputs, Limits{})
		if got.Selected != expected["known_head"] || got.SelectedBy != "issued_at" || !got.Forked || len(got.Leaves) != 2 {
			t.Fatalf("graph selection/leaves/edges = %q/%q/%v/%v/%#v", got.Selected, got.SelectedBy, got.Forked, got.Leaves, got.Edges)
		}
		if got.Leaves[0] != expected["known_head"] || got.Leaves[1] != "fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b" {
			t.Fatalf("leaves = %v", got.Leaves)
		}
		if i := sort.SearchStrings(got.Nodes, messages[4].MessageID); i < len(got.Nodes) && got.Nodes[i] == messages[4].MessageID {
			t.Fatalf("reply unexpectedly appears in version nodes: %v", got.Nodes)
		}
	})
	t.Run("maximum node bound truncates", func(t *testing.T) {
		messages, _ := asVersionFixture(t)
		got := BuildVersionGraph(messages[0].MessageID, []VersionInput{{Message: messages[0]}, {Message: messages[1]}, {Message: messages[2]}, {Message: messages[3]}}, Limits{MaxNodes: 2})
		if !got.Truncated || len(got.Nodes) > 2 || len(got.Edges) > 2 {
			t.Fatal("Truncated = false")
		}
	})
	t.Run("unknown start is empty", func(t *testing.T) {
		got := BuildVersionGraph("absent", nil, Limits{})
		if len(got.Nodes) != 0 {
			t.Fatalf("Nodes = %v", got.Nodes)
		}
	})
	t.Run("root signed leaf beats a later delegate leaf", func(t *testing.T) {
		root := wire.PostMessage{MessageID: "root", AuthorID: "author"}
		delegate := wire.PostMessage{MessageID: "fy1:b", AuthorID: "author", SignerID: "delegate", Payload: wire.PostPayload{Supersedes: "root"}}
		direct := wire.PostMessage{MessageID: "fy1:a", AuthorID: "author", Payload: wire.PostPayload{Supersedes: "root"}}
		got := BuildVersionGraph("root", []VersionInput{{Message: root}, {Message: delegate, IssuedAt: time.Unix(20, 0)}, {Message: direct, IssuedAt: time.Unix(10, 0)}}, Limits{})
		if got.Selected != "fy1:a" || got.SelectedBy != "root_signed" {
			t.Fatalf("selection = %q by %q", got.Selected, got.SelectedBy)
		}
	})
	t.Run("root signed leaf beats an earlier delegate leaf", func(t *testing.T) {
		root := wire.PostMessage{MessageID: "root", AuthorID: "author"}
		delegate := wire.PostMessage{MessageID: "fy1:b", AuthorID: "author", SignerID: "delegate", Payload: wire.PostPayload{Supersedes: "root"}}
		direct := wire.PostMessage{MessageID: "fy1:a", AuthorID: "author", Payload: wire.PostPayload{Supersedes: "root"}}
		got := BuildVersionGraph("root", []VersionInput{{Message: root}, {Message: delegate, IssuedAt: time.Unix(10, 0)}, {Message: direct, IssuedAt: time.Unix(20, 0)}}, Limits{})
		if got.Selected != "fy1:a" || got.SelectedBy != "root_signed" {
			t.Fatalf("selection = %q by %q", got.Selected, got.SelectedBy)
		}
	})
	t.Run("timestamp tie selects ASCII smallest ID", func(t *testing.T) {
		root := wire.PostMessage{MessageID: "root", AuthorID: "author"}
		a := wire.PostMessage{MessageID: "fy1:a", AuthorID: "author", Payload: wire.PostPayload{Supersedes: "root"}}
		b := wire.PostMessage{MessageID: "fy1:b", AuthorID: "author", Payload: wire.PostPayload{Supersedes: "root"}}
		time := time.Unix(10, 0)
		got := BuildVersionGraph("root", []VersionInput{{Message: root}, {Message: b, IssuedAt: time}, {Message: a, IssuedAt: time}}, Limits{})
		if got.Selected != "fy1:a" || got.SelectedBy != "message_id" {
			t.Fatalf("selection = %q by %q", got.Selected, got.SelectedBy)
		}
	})
	t.Run("unknown predecessor remains unresolved", func(t *testing.T) {
		update := wire.PostMessage{MessageID: "fy1:update", AuthorID: "author", Payload: wire.PostPayload{Supersedes: "fy1:missing"}}
		got := BuildVersionGraph(update.MessageID, []VersionInput{{Message: update}}, Limits{})
		if len(got.Edges) != 1 || got.Edges[0].State != EdgeUnresolved || got.Leaves[0] != update.MessageID {
			t.Fatalf("graph = %#v", got)
		}
	})
	t.Run("author mismatch rejects edge and preserves both nodes", func(t *testing.T) {
		parent := wire.PostMessage{MessageID: "fy1:parent", AuthorID: "old"}
		update := wire.PostMessage{MessageID: "fy1:update", AuthorID: "new", Payload: wire.PostPayload{Supersedes: parent.MessageID}}
		got := BuildVersionGraph(update.MessageID, []VersionInput{{Message: parent}, {Message: update}}, Limits{})
		if len(got.Nodes) != 2 || len(got.Edges) != 1 || got.Edges[0].State != EdgeRejected || got.Edges[0].Reason != "author_mismatch" {
			t.Fatalf("graph = %#v", got)
		}
	})
	t.Run("cycle edge is rejected", func(t *testing.T) {
		a := wire.PostMessage{MessageID: "fy1:a", AuthorID: "author", Payload: wire.PostPayload{Supersedes: "fy1:b"}}
		b := wire.PostMessage{MessageID: "fy1:b", AuthorID: "author", Payload: wire.PostPayload{Supersedes: "fy1:a"}}
		got := BuildVersionGraph(a.MessageID, []VersionInput{{Message: a}, {Message: b}}, Limits{})
		if len(got.Nodes) != 2 || got.Truncated || len(got.Edges) != 2 {
			t.Fatalf("graph = %#v", got)
		}
		if got.Edges[0].State != EdgeVerified || got.Edges[1].State != EdgeRejected || got.Edges[1].Reason != "cycle" || len(got.Leaves) != 1 || got.Selected != got.Leaves[0] || got.Selected == "" {
			t.Fatalf("cycle result = %#v", got)
		}
	})
}
