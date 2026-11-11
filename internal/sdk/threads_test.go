package sdk

import (
	"encoding/json"
	"os"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func asThreadFixture(t *testing.T) []wire.PostMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/as/post-comment-thread.json")
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

func TestBuildThread(t *testing.T) {
	t.Run("fixture nests replies by exact parent", func(t *testing.T) {
		ms := asThreadFixture(t)
		root := ms[0]
		got := BuildThread(root.MessageID, &root, ms[1:], Limits{})
		if got.Root == nil || len(got.Root.Children) != 1 || got.Root.Children[0].Depth != 1 || len(got.Root.Children[0].Children) != 1 || got.Root.Children[0].Children[0].Depth != 2 {
			t.Fatalf("thread shape = %#v", got.Root)
		}
	})
	t.Run("missing root keeps children under unresolved parent", func(t *testing.T) {
		ms := asThreadFixture(t)
		got := BuildThread(ms[0].MessageID, nil, ms[1:], Limits{})
		if len(got.Unresolved[ms[0].MessageID]) != 1 {
			t.Fatalf("unresolved root children = %d", len(got.Unresolved[ms[0].MessageID]))
		}
		first := got.Unresolved[ms[0].MessageID][0]
		if len(first.Children) != 1 || first.Children[0].Depth != 2 {
			t.Fatalf("missing-root branch = %#v", first)
		}
		if got.Root.Message.Payload.Text != "" {
			t.Fatalf("placeholder root has content %q", got.Root.Message.Payload.Text)
		}
	})
	t.Run("duplicate candidate is included once and self-reference is excluded", func(t *testing.T) {
		ms := asThreadFixture(t)
		dup := ms[1]
		self := wire.PostMessage{MessageID: "fy1:self", Payload: wire.PostPayload{ReplyTo: "fy1:self"}}
		got := BuildThread(ms[0].MessageID, &ms[0], []wire.PostMessage{dup, dup, self}, Limits{})
		if len(got.Root.Children) != 1 || got.Root.Children[0].Message.MessageID != dup.MessageID {
			t.Fatalf("root children = %#v", got.Root.Children)
		}
	})
	t.Run("reply context retains signed target", func(t *testing.T) {
		messages, expected := asVersionFixture(t)
		reply := messages[4]
		if reply.MessageID == "" {
			t.Fatal("fixture reply not found")
		}
		got := BuildThread(expected["reply_target"], nil, []wire.PostMessage{reply}, Limits{})
		AnnotateVersions(&got, func(id string) (string, bool) {
			return expected["known_head"], id == expected["reply_target"]
		})
		node := got.Unresolved[expected["reply_target"]][0]
		if !node.ContextChanged || node.RepliedTo != expected["reply_target"] {
			t.Fatalf("reply context = changed %v, target %q", node.ContextChanged, node.RepliedTo)
		}
	})
}
