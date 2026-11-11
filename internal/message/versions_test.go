package message

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// versionPosts loads testdata/post-version-chain.json, a copy of examples/post-version-chain.json in the specification repository.
func versionPosts(t *testing.T) [5]wire.PostMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/post-version-chain.json")
	if err != nil {
		t.Fatalf("read fixture copied from specification repository examples/post-version-chain.json: %v", err)
	}
	document, err := canon.Parse(raw, canon.MessageLimits)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal parsed fixture: %v", err)
	}
	var envelope struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatalf("decode fixture envelope: %v", err)
	}
	if len(envelope.Messages) < 5 {
		t.Fatalf("fixture contains %d messages; want at least 5", len(envelope.Messages))
	}
	return [5]wire.PostMessage{
		versionPost(t, envelope.Messages[0]),
		versionPost(t, envelope.Messages[1]),
		versionPost(t, envelope.Messages[2]),
		versionPost(t, envelope.Messages[3]),
		versionPost(t, envelope.Messages[4]),
	}
}

func versionPost(t *testing.T, raw json.RawMessage) wire.PostMessage {
	t.Helper()
	value, err := canon.Parse(raw, canon.MessageLimits)
	if err != nil {
		t.Fatalf("parse post: %v", err)
	}
	if err := wire.Check("PostMessage", value); err != nil {
		t.Fatalf("check post schema: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal post value: %v", err)
	}
	var post wire.PostMessage
	if err := json.Unmarshal(encoded, &post); err != nil {
		t.Fatalf("decode post: %v", err)
	}
	return post
}

func versionIntent(post wire.PostMessage) wire.Intent {
	return wire.Intent{
		AuthorID: post.AuthorID, SignerID: post.SignerID, Delegation: post.Delegation,
		Timestamp: post.Timestamp, MessageType: post.MessageType, Version: post.Version, Payload: post.Payload,
	}
}

func TestVersionEdge(t *testing.T) {
	posts := versionPosts(t)
	t.Run("original", func(t *testing.T) {
		if got := Edge(posts[0], nil); got != Original {
			t.Fatalf("Edge(root, nil) = %s; want original", got)
		}
	})
	t.Run("valid update", func(t *testing.T) {
		if got := Edge(posts[1], &posts[0]); got != ValidEdge {
			t.Fatalf("Edge(update, target) = %s; want valid", got)
		}
	})
	t.Run("missing target is unresolved", func(t *testing.T) {
		if got := Edge(posts[1], nil); got != Unresolved {
			t.Fatalf("Edge(update, nil) = %s; want unresolved", got)
		}
	})
	t.Run("target with a different ID is unresolved", func(t *testing.T) {
		if got := Edge(posts[2], &posts[0]); got != Unresolved {
			t.Fatalf("Edge(nonmatching target) = %s; want unresolved", got)
		}
	})
	t.Run("cross-author target is invalid", func(t *testing.T) {
		modified := posts[0]
		modified.AuthorID = posts[4].AuthorID
		if got := Edge(posts[1], &modified); got != InvalidEdge {
			t.Fatalf("Edge(cross-author target) = %s; want invalid", got)
		}
	})
	t.Run("reply target mismatch is invalid", func(t *testing.T) {
		modified := posts[0]
		modified.Payload.ReplyTo = posts[4].MessageID
		if got := Edge(posts[1], &modified); got != InvalidEdge {
			t.Fatalf("Edge(reply mismatch) = %s; want invalid", got)
		}
	})
	t.Run("self-reference is invalid", func(t *testing.T) {
		modified := posts[1]
		modified.Payload.Supersedes = modified.MessageID
		if got := Edge(modified, &posts[0]); got != InvalidEdge {
			t.Fatalf("Edge(self-reference) = %s; want invalid", got)
		}
	})
}

func TestVersionCheckAdmissionEdge(t *testing.T) {
	posts := versionPosts(t)
	intent := versionIntent(posts[1])
	t.Run("known matching target", func(t *testing.T) {
		if err := CheckAdmissionEdge(intent, posts[1].MessageID, &posts[0]); err != nil {
			t.Fatalf("CheckAdmissionEdge() error = %v", err)
		}
	})
	t.Run("unknown target", func(t *testing.T) {
		if err := CheckAdmissionEdge(intent, posts[1].MessageID, nil); err != nil {
			t.Fatalf("CheckAdmissionEdge() error = %v", err)
		}
	})
	t.Run("known cross-author target rejected before payment", func(t *testing.T) {
		modified := posts[0]
		modified.AuthorID = posts[4].AuthorID
		err := CheckAdmissionEdge(intent, posts[1].MessageID, &modified)
		if !errors.Is(err, ErrInvalidEdge) {
			t.Fatalf("CheckAdmissionEdge() error = %v; want ErrInvalidEdge", err)
		}
		if status, ok := apierr.StatusOf(err); !ok || status != apierr.InvalidMessage {
			t.Fatalf("public status = %q, %v; want invalid_message", status, ok)
		}
	})
	t.Run("known reply mismatch rejected", func(t *testing.T) {
		modified := posts[0]
		modified.Payload.ReplyTo = posts[4].MessageID
		if err := CheckAdmissionEdge(intent, posts[1].MessageID, &modified); !errors.Is(err, ErrInvalidEdge) {
			t.Fatalf("CheckAdmissionEdge() error = %v; want ErrInvalidEdge", err)
		}
	})
	t.Run("self-reference rejected", func(t *testing.T) {
		self := intent
		self.Payload.Supersedes = posts[1].MessageID
		if err := CheckAdmissionEdge(self, posts[1].MessageID, nil); !errors.Is(err, ErrInvalidEdge) {
			t.Fatalf("CheckAdmissionEdge() error = %v; want ErrInvalidEdge", err)
		}
	})
}

func TestVersionRootSigned(t *testing.T) {
	posts := versionPosts(t)
	t.Run("implicit root signer", func(t *testing.T) {
		if !RootSigned(posts[0]) {
			t.Fatal("RootSigned(root) = false; want true")
		}
	})
	t.Run("delegate signer", func(t *testing.T) {
		delegated := posts[1]
		delegated.SignerID = posts[4].AuthorID
		delegated.Delegation = &wire.Delegation{}
		if RootSigned(delegated) {
			t.Fatal("RootSigned(delegate) = true; want false")
		}
	})
}

func TestVersionSelectLeaf(t *testing.T) {
	posts := versionPosts(t)
	t.Run("root signer beats later delegate", func(t *testing.T) {
		rootLeaf := posts[1]
		rootLeaf.SubmissionReceipt.IssuedAt = "2026-11-11T00:01:00Z"
		delegateLeaf := posts[2]
		delegateLeaf.SignerID = posts[4].AuthorID
		delegateLeaf.Delegation = &wire.Delegation{}
		delegateLeaf.SubmissionReceipt.IssuedAt = "2026-11-11T09:00:00Z"
		got, err := SelectLeaf([]wire.PostMessage{delegateLeaf, rootLeaf})
		if err != nil || got.MessageID != rootLeaf.MessageID {
			t.Fatalf("SelectLeaf() = %q, %v; want root leaf %q", got.MessageID, err, rootLeaf.MessageID)
		}
	})
	t.Run("lone delegate is selected", func(t *testing.T) {
		delegate := posts[1]
		delegate.SignerID = posts[4].AuthorID
		delegate.Delegation = &wire.Delegation{}
		got, err := SelectLeaf([]wire.PostMessage{delegate})
		if err != nil || got.MessageID != delegate.MessageID {
			t.Fatalf("SelectLeaf() = %q, %v; want %q", got.MessageID, err, delegate.MessageID)
		}
	})
	t.Run("equal receipt time uses ASCII-smallest ID", func(t *testing.T) {
		a := posts[1]
		b := posts[2]
		a.SubmissionReceipt.IssuedAt = "2026-11-11T00:01:00Z"
		b.SubmissionReceipt.IssuedAt = a.SubmissionReceipt.IssuedAt
		got, err := SelectLeaf([]wire.PostMessage{b, a})
		if err != nil || got.MessageID != a.MessageID {
			t.Fatalf("SelectLeaf() = %q, %v; want smallest ID %q", got.MessageID, err, a.MessageID)
		}
	})
	t.Run("later receipt beats smaller ID", func(t *testing.T) {
		earlier := posts[1]
		later := posts[2]
		earlier.SubmissionReceipt.IssuedAt = "2026-11-11T00:01:00Z"
		later.SubmissionReceipt.IssuedAt = "2026-11-11T00:02:00Z"
		got, err := SelectLeaf([]wire.PostMessage{later, earlier})
		if err != nil || got.MessageID != later.MessageID {
			t.Fatalf("SelectLeaf() = %q, %v; want later receipt %q", got.MessageID, err, later.MessageID)
		}
	})
	t.Run("backdated author timestamp with a later receipt still wins", func(t *testing.T) {
		earlier := posts[1]
		later := posts[2]
		earlier.SubmissionReceipt.IssuedAt = "2026-11-11T00:01:00Z"
		later.SubmissionReceipt.IssuedAt = "2026-11-11T00:02:00Z"
		later.Timestamp = "2026-11-10T00:00:00Z"
		got, err := SelectLeaf([]wire.PostMessage{later, earlier})
		if err != nil || got.MessageID != later.MessageID {
			t.Fatalf("SelectLeaf() = %q, %v; want later receipt %q", got.MessageID, err, later.MessageID)
		}
	})
	t.Run("empty input is an error", func(t *testing.T) {
		if _, err := SelectLeaf(nil); err == nil {
			t.Fatal("SelectLeaf(nil) succeeded; want error")
		}
	})
}

func TestVersionResolveExample(t *testing.T) {
	posts := versionPosts(t)
	wantHead := "fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf"
	wantLeaves := []string{
		"fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf",
		"fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b",
	}
	wantMembers := []string{
		"fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72",
		"fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f",
		"fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf",
		"fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b",
	}
	check := func(t *testing.T, known []wire.PostMessage) {
		t.Helper()
		chain, err := Resolve(posts[0], known, DefaultLimits)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if chain.Head.MessageID != wantHead || !chain.Forked || len(chain.Leaves) != 2 {
			t.Fatalf("Resolve() head=%q forked=%v leaves=%v", chain.Head.MessageID, chain.Forked, chain.Leaves)
		}
		if chain.Leaves[0] != wantLeaves[0] || chain.Leaves[1] != wantLeaves[1] {
			t.Fatalf("Resolve() leaves=%v; want %v", chain.Leaves, wantLeaves)
		}
		if !slices.Equal(chain.Members, wantMembers) {
			t.Fatalf("Resolve() members=%v; want %v", chain.Members, wantMembers)
		}
		if slices.Contains(chain.Members, posts[4].MessageID) {
			t.Fatalf("reply %q was included in version component", posts[4].MessageID)
		}
	}
	t.Run("specification order", func(t *testing.T) {
		check(t, []wire.PostMessage{posts[0], posts[1], posts[2], posts[3], posts[4]})
	})
	t.Run("reversed input order", func(t *testing.T) {
		check(t, []wire.PostMessage{posts[4], posts[3], posts[2], posts[1], posts[0]})
	})
}

func TestVersionResolveCollisionAndDuplicate(t *testing.T) {
	posts := versionPosts(t)
	t.Run("different intents with one ID collide", func(t *testing.T) {
		changed := posts[1]
		changed.Payload.Text = "different intent"
		if _, err := Resolve(posts[0], []wire.PostMessage{posts[1], changed}, DefaultLimits); !errors.Is(err, ErrIDCollision) {
			t.Fatalf("Resolve() error = %v; want ErrIDCollision", err)
		}
	})
	t.Run("different receipt for same intent is one version", func(t *testing.T) {
		duplicate := posts[1]
		duplicate.SubmissionReceipt.ReceiptID = "different-receipt"
		chain, err := Resolve(posts[0], []wire.PostMessage{posts[1], duplicate}, DefaultLimits)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if len(chain.Leaves) != 1 || chain.Forked {
			t.Fatalf("Resolve() leaves=%v forked=%v; want one leaf", chain.Leaves, chain.Forked)
		}
	})
}

func TestVersionResolveBoundsAndCycles(t *testing.T) {
	posts := versionPosts(t)
	t.Run("cycle", func(t *testing.T) {
		a := posts[1]
		b := posts[2]
		a.MessageID = "A"
		b.MessageID = "B"
		a.Payload.Supersedes = "B"
		b.Payload.Supersedes = "A"
		if _, err := Resolve(a, []wire.PostMessage{b}, DefaultLimits); !errors.Is(err, ErrCycle) {
			t.Fatalf("Resolve() error = %v; want ErrCycle", err)
		}
	})
	t.Run("node limit", func(t *testing.T) {
		limits := Limits{MaxNodes: 2, MaxDepth: 1000, MaxFanout: 1000}
		if _, err := Resolve(posts[0], []wire.PostMessage{posts[0], posts[1], posts[2]}, limits); !errors.Is(err, ErrWorkLimit) {
			t.Fatalf("Resolve() error = %v; want ErrWorkLimit", err)
		}
	})
	t.Run("depth limit rejects two edges at depth one", func(t *testing.T) {
		limits := Limits{MaxNodes: 1000, MaxDepth: 1, MaxFanout: 1000}
		if _, err := Resolve(posts[0], []wire.PostMessage{posts[1], posts[2]}, limits); !errors.Is(err, ErrWorkLimit) {
			t.Fatalf("Resolve() error = %v; want ErrWorkLimit", err)
		}
	})
	t.Run("depth limit accepts two edges at depth two", func(t *testing.T) {
		limits := Limits{MaxNodes: 1000, MaxDepth: 2, MaxFanout: 1000}
		if _, err := Resolve(posts[0], []wire.PostMessage{posts[1], posts[2]}, limits); err != nil {
			t.Fatalf("Resolve() error = %v; want nil", err)
		}
	})
	t.Run("fanout limit", func(t *testing.T) {
		limits := Limits{MaxNodes: 1000, MaxDepth: 1000, MaxFanout: 1}
		if _, err := Resolve(posts[0], []wire.PostMessage{posts[1], posts[3]}, limits); !errors.Is(err, ErrWorkLimit) {
			t.Fatalf("Resolve() error = %v; want ErrWorkLimit", err)
		}
	})
}
