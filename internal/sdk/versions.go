package sdk

import (
	"sort"
	"time"

	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

// Limits bounds local graph assembly. Zero or negative fields use the documented defaults.
type Limits struct{ MaxNodes, MaxDepth int }

// VersionInput describes a record that has already passed full verification.
type VersionInput struct {
	Message    wire.PostMessage
	IssuedAt   time.Time
	RootSigned bool
}

// EdgeState identifies the local status of a supersedes edge.
type EdgeState int

const (
	// EdgeVerified is a valid same-author, same-reply version edge.
	EdgeVerified EdgeState = iota
	// EdgeUnresolved names a predecessor not present in the known set.
	EdgeUnresolved
	// EdgeRejected is an invalid edge that does not invalidate either record.
	EdgeRejected
)

// VersionEdge records the relation claimed by From to To.
type VersionEdge struct {
	From, To string
	State    EdgeState
	Reason   string
}

// VersionGraph is a bounded local view of verified records. Selection is a local default over known records, not proof of the globally latest version or the author's preference.
type VersionGraph struct {
	Nodes, Leaves        []string
	Edges                []VersionEdge
	Selected, SelectedBy string
	Forked, Truncated    bool
}

// BuildVersionGraph assembles the verified component containing start from records already fully verified. Duplicate IDs keep their earliest IssuedAt occurrence. An unknown start returns an empty graph.
func BuildVersionGraph(start string, known []VersionInput, lim Limits) VersionGraph {
	if lim.MaxNodes <= 0 {
		lim.MaxNodes = 256
	}
	if lim.MaxDepth <= 0 {
		lim.MaxDepth = 64
	}
	byID := make(map[string]VersionInput, len(known))
	for _, in := range known {
		old, ok := byID[in.Message.MessageID]
		if !ok || in.IssuedAt.Before(old.IssuedAt) {
			byID[in.Message.MessageID] = in
		}
	}
	if _, ok := byID[start]; !ok {
		return VersionGraph{}
	}
	children := make(map[string][]string)
	for id, in := range byID {
		if in.Message.Payload.Supersedes != "" {
			children[in.Message.Payload.Supersedes] = append(children[in.Message.Payload.Supersedes], id)
		}
	}
	for parent := range children {
		sort.Strings(children[parent])
	}
	cycleRejected := make(map[string]bool)
	colors := make(map[string]uint8, len(byID))
	cycleIDs := make([]string, 0, len(byID))
	for id := range byID {
		cycleIDs = append(cycleIDs, id)
	}
	sort.Strings(cycleIDs)
	for _, startID := range cycleIDs {
		if colors[startID] != 0 {
			continue
		}
		path := make([]string, 0)
		current := startID
		for current != "" && colors[current] == 0 {
			colors[current] = 1
			path = append(path, current)
			in := byID[current]
			target, ok := byID[in.Message.Payload.Supersedes]
			if !ok || message.Edge(in.Message, &target.Message) != message.ValidEdge {
				current = ""
				break
			}
			current = in.Message.Payload.Supersedes
		}
		if current != "" && colors[current] == 1 && len(path) > 0 {
			cycleRejected[path[len(path)-1]] = true
		}
		for _, id := range path {
			colors[id] = 2
		}
	}
	result := VersionGraph{}
	listed := map[string]bool{}
	visited := map[string]bool{}
	component := map[string]bool{}
	type visit struct {
		id    string
		depth int
	}
	stack := []visit{{id: start}}
	verifiedSuccessor := make(map[string]bool)
	edgeSeen := make(map[string]bool)
	appendEdge := func(edge VersionEdge) {
		key := edge.From + "\x00" + edge.To
		if edgeSeen[key] {
			return
		}
		edgeSeen[key] = true
		if len(result.Edges) < lim.MaxNodes {
			result.Edges = append(result.Edges, edge)
		} else {
			result.Truncated = true
		}
	}
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[v.id] {
			continue
		}
		if len(visited) >= lim.MaxNodes {
			result.Truncated = true
			break
		}
		if v.depth > lim.MaxDepth {
			result.Truncated = true
			continue
		}
		in := byID[v.id]
		visited[v.id] = true
		listed[v.id] = true
		component[v.id] = true
		parent := in.Message.Payload.Supersedes
		if parent != "" {
			target, ok := byID[parent]
			edge := VersionEdge{From: v.id, To: parent}
			if !ok {
				edge.State = EdgeUnresolved
			} else {
				if cycleRejected[v.id] {
					edge.State = EdgeRejected
					edge.Reason = "cycle"
				} else {
					switch message.Edge(in.Message, &target.Message) {
					case message.ValidEdge:
						edge.State = EdgeVerified
						verifiedSuccessor[parent] = true
						stack = append(stack, visit{id: parent, depth: v.depth + 1})
					case message.Unresolved:
						edge.State = EdgeUnresolved
					case message.InvalidEdge:
						edge.State = EdgeRejected
						if parent == v.id {
							edge.Reason = "self_reference"
						} else if target.Message.AuthorID != in.Message.AuthorID {
							edge.Reason = "author_mismatch"
						} else {
							edge.Reason = "reply_to_mismatch"
						}
					}
				}
			}
			appendEdge(edge)
			if ok && edge.State == EdgeRejected && !listed[parent] {
				if len(listed) < lim.MaxNodes {
					listed[parent] = true
				} else {
					result.Truncated = true
				}
			}
		}
		for _, childID := range children[v.id] {
			child := byID[childID]
			status := message.Edge(child.Message, &in.Message)
			if cycleRejected[childID] {
				edge := VersionEdge{From: childID, To: v.id, State: EdgeRejected, Reason: "cycle"}
				appendEdge(edge)
				if !listed[childID] {
					if len(listed) < lim.MaxNodes {
						listed[childID] = true
					} else {
						result.Truncated = true
					}
				}
				continue
			}
			if status == message.InvalidEdge {
				edge := VersionEdge{From: childID, To: v.id, State: EdgeRejected}
				if childID == v.id {
					edge.Reason = "self_reference"
				} else if child.Message.AuthorID != in.Message.AuthorID {
					edge.Reason = "author_mismatch"
				} else {
					edge.Reason = "reply_to_mismatch"
				}
				appendEdge(edge)
				if !listed[childID] {
					if len(listed) < lim.MaxNodes {
						listed[childID] = true
					} else {
						result.Truncated = true
					}
				}
				continue
			}
			if status != message.ValidEdge {
				continue
			}
			if !visited[childID] {
				stack = append(stack, visit{id: childID, depth: v.depth + 1})
			}
		}
	}
	for id := range listed {
		result.Nodes = append(result.Nodes, id)
	}
	sort.Strings(result.Nodes)
	for _, in := range byID {
		target, ok := byID[in.Message.Payload.Supersedes]
		if ok && !cycleRejected[in.Message.MessageID] && message.Edge(in.Message, &target.Message) == message.ValidEdge {
			verifiedSuccessor[in.Message.Payload.Supersedes] = true
		}
	}
	for id := range component {
		if !verifiedSuccessor[id] {
			result.Leaves = append(result.Leaves, id)
		}
	}
	sort.Strings(result.Leaves)
	result.Forked = len(result.Leaves) > 1
	if len(result.Leaves) == 1 {
		result.Selected = result.Leaves[0]
		result.SelectedBy = "only_leaf"
	} else if len(result.Leaves) > 1 {
		pool := append([]string(nil), result.Leaves...)
		keepRoot := false
		for _, id := range pool {
			if byID[id].RootSigned {
				keepRoot = true
			}
		}
		if keepRoot {
			filtered := pool[:0]
			for _, id := range pool {
				if byID[id].RootSigned {
					filtered = append(filtered, id)
				}
			}
			pool = filtered
			if len(pool) == 1 {
				result.SelectedBy = "root_signed"
			}
		}
		if result.SelectedBy == "" {
			latest := pool[0]
			for _, id := range pool[1:] {
				if byID[id].IssuedAt.After(byID[latest].IssuedAt) {
					latest = id
				}
			}
			filtered := pool[:0]
			for _, id := range pool {
				if byID[id].IssuedAt.Equal(byID[latest].IssuedAt) {
					filtered = append(filtered, id)
				}
			}
			pool = filtered
			if len(pool) == 1 {
				result.SelectedBy = "issued_at"
			}
		}
		if result.SelectedBy == "" {
			sort.Strings(pool)
			result.SelectedBy = "message_id"
		}
		result.Selected = pool[0]
	}
	return result
}
