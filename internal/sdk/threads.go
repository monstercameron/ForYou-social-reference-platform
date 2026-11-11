package sdk

import (
	"sort"

	"foryou.local/foryou/internal/wire"
)

func asThreadLess(a, b wire.PostMessage) bool {
	ta, errA := wire.ParseTime(a.Timestamp)
	tb, errB := wire.ParseTime(b.Timestamp)
	if errA == nil && errB == nil && !ta.Equal(tb) {
		return ta.Before(tb)
	}
	return a.MessageID < b.MessageID
}

// ThreadNode is a verified message in a locally assembled reply tree.
type ThreadNode struct {
	Message        wire.PostMessage
	Children       []*ThreadNode
	Depth          int
	RepliedTo      string
	ContextChanged bool
}

// Thread is a bounded view of the verified records held by the caller. Counts derived from it are not network totals.
type Thread struct {
	Root       *ThreadNode
	Unresolved map[string][]*ThreadNode
	Truncated  bool
}

// BuildThread assembles candidates that already passed full verification. The result is only a local view of records the caller holds; parent display data comes only from the parent message and counts are not network totals.
func BuildThread(rootID string, root *wire.PostMessage, candidates []wire.PostMessage, lim Limits) Thread {
	if lim.MaxNodes <= 0 {
		lim.MaxNodes = 512
	}
	if lim.MaxDepth <= 0 {
		lim.MaxDepth = 8
	}
	t := Thread{Unresolved: make(map[string][]*ThreadNode)}
	byID := make(map[string]wire.PostMessage, len(candidates)+1)
	if root != nil {
		byID[rootID] = *root
		t.Root = &ThreadNode{Message: *root, Depth: 0, RepliedTo: root.Payload.ReplyTo}
	} else {
		t.Root = &ThreadNode{Message: wire.PostMessage{MessageID: rootID}, Depth: 0}
	}
	for _, m := range candidates {
		if m.MessageID == rootID {
			continue
		}
		if m.Payload.ReplyTo == m.MessageID {
			continue
		}
		if _, ok := byID[m.MessageID]; !ok {
			byID[m.MessageID] = m
		}
	}
	children := make(map[string][]wire.PostMessage)
	for id, m := range byID {
		if id != rootID && m.Payload.ReplyTo != "" {
			children[m.Payload.ReplyTo] = append(children[m.Payload.ReplyTo], m)
		}
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool {
			return asThreadLess(children[parent][i], children[parent][j])
		})
	}
	attached := map[string]bool{}
	if _, ok := byID[rootID]; ok {
		attached[rootID] = true
	}
	count := 1
	type pending struct {
		parent   *ThreadNode
		parentID string
		depth    int
	}
	queue := []pending{{parent: t.Root, parentID: rootID, depth: 0}}
	orphanIDs := make([]string, 0)
	for id, m := range byID {
		if id != rootID && m.Payload.ReplyTo != "" && byID[m.Payload.ReplyTo].MessageID == "" {
			orphanIDs = append(orphanIDs, id)
		}
	}
	sort.Strings(orphanIDs)
	for _, id := range orphanIDs {
		m := byID[id]
		if count >= lim.MaxNodes {
			t.Truncated = true
			continue
		}
		node := &ThreadNode{Message: m, Depth: 1, RepliedTo: m.Payload.ReplyTo}
		t.Unresolved[m.Payload.ReplyTo] = append(t.Unresolved[m.Payload.ReplyTo], node)
		attached[id] = true
		count++
		queue = append(queue, pending{parent: node, parentID: id, depth: 1})
	}
	for head := 0; head < len(queue); head++ {
		p := queue[head]
		for _, m := range children[p.parentID] {
			if attached[m.MessageID] {
				continue
			}
			if p.depth+1 > lim.MaxDepth || count >= lim.MaxNodes {
				t.Truncated = true
				continue
			}
			node := &ThreadNode{Message: m, Depth: p.depth + 1, RepliedTo: m.Payload.ReplyTo}
			attached[m.MessageID] = true
			count++
			if _, ok := byID[p.parentID]; ok {
				p.parent.Children = append(p.parent.Children, node)
			} else {
				t.Unresolved[p.parentID] = append(t.Unresolved[p.parentID], node)
			}
			queue = append(queue, pending{parent: node, parentID: m.MessageID, depth: node.Depth})
		}
	}
	for key := range t.Unresolved {
		sort.Slice(t.Unresolved[key], func(i, j int) bool {
			return asThreadLess(t.Unresolved[key][i].Message, t.Unresolved[key][j].Message)
		})
	}
	return t
}

// AnnotateVersions marks replies whose signed parent version differs from the locally selected chain version.
func AnnotateVersions(t *Thread, selectedFor func(id string) (string, bool)) {
	if t == nil || t.Root == nil || selectedFor == nil {
		return
	}
	stack := []*ThreadNode{t.Root}
	for _, nodes := range t.Unresolved {
		stack = append(stack, nodes...)
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.RepliedTo != "" {
			selected, ok := selectedFor(n.RepliedTo)
			n.ContextChanged = ok && selected != n.RepliedTo
		}
		stack = append(stack, n.Children...)
	}
}
