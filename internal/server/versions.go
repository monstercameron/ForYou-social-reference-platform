package server

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

const (
	// verMaxDepth bounds the predecessor walk and the graph depth; verMaxNodes bounds every search for versions.
	verMaxDepth = 64
	verMaxNodes = 256
)

// verRules states, in words, why the default selection chose its leaf.
var verRules = map[string]string{
	"root_signed": "Signed by the author's own key, which outranks delegate-signed versions",
	"issued_at":   "Latest provider receipt time among the known versions",
	"message_id":  "Tie broken by ID order",
	"only_leaf":   "Only known latest version",
}

func init() { routeGroups = append(routeGroups, registerVersions) }

func registerVersions(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /posts/{id}/versions", a.versionsHandler)
	mux.HandleFunc("GET /posts/{id}/versions/fragment", a.versionsFragmentHandler)
}

type versionsPage struct {
	Layout   Layout
	Versions verHistory
}

// verHistory is the version history of one post as the reader's sources returned it.
type verHistory struct {
	ID string
	// Notice replaces the history when no verified chain can be shown.
	Notice string
	// Selected is the default version; Rule says why it was chosen.
	Selected post
	Rule     string
	// Leaves counts the competing latest versions when the history forks, and is zero otherwise.
	Leaves    int
	Tree      []verNode
	Truncated bool
	Shown     int
	Sources   []string
	// PublishURL links to a new version when the reader is the author.
	PublishURL string
}

// verNode is one version row. Children are the versions that supersede it.
type verNode struct {
	ID, Short, When, Signer, FirstLine string
	Post                               post
	Selected, Leaf, Branch             bool
	// ChildLabel names the list of branches that start after this version.
	ChildLabel string
	// Missing is an earlier version the sources did not return; Rejected says why the link to the earlier version is invalid.
	Missing, Rejected string
	Children          []verNode
}

// versionHistory gathers the verified versions around one post, bounded in depth and size, and assembles the local graph.
func (a *App) versionHistory(w http.ResponseWriter, r *http.Request, id string) (verHistory, int) {
	ctx := r.Context()
	source := a.records()
	start, coverage, found, err := source.Get(ctx, r, id)
	if err != nil {
		return verHistory{}, http.StatusInternalServerError
	}
	if !found {
		return verHistory{}, http.StatusNotFound
	}
	history := verHistory{ID: id, Sources: coverage.Lines()}
	if !verVerified(start) {
		history.Notice = "This post is not a verified network record, so its version history cannot be checked."
		return history, http.StatusOK
	}
	known := map[string]post{start.ID: start}
	chain := []string{start.ID}
	walkTruncated := false
	for current := start; current.Supersedes != ""; {
		if len(chain) >= verMaxDepth {
			walkTruncated = true
			break
		}
		earlier, _, ok, err := source.Get(ctx, r, current.Supersedes)
		if err != nil {
			return verHistory{}, http.StatusInternalServerError
		}
		if _, seen := known[current.Supersedes]; seen || !ok || earlier.ID != current.Supersedes || !verVerified(earlier) {
			break
		}
		known[earlier.ID] = earlier
		chain = append(chain, earlier.ID)
		current = earlier
	}
	budgetReached := false
	for queue := slices.Clone(chain); len(queue) > 0 && !budgetReached; queue = queue[1:] {
		page, err := source.Successors(ctx, r, queue[0])
		if err != nil {
			return verHistory{}, http.StatusInternalServerError
		}
		for _, line := range verLookupGaps(page.Coverage) {
			if !slices.Contains(history.Sources, line) {
				history.Sources = append(history.Sources, line)
			}
		}
		for _, next := range page.Posts {
			if _, seen := known[next.ID]; seen || !verVerified(next) {
				continue
			}
			if len(known) == verMaxNodes {
				budgetReached = true
				break
			}
			known[next.ID] = next
			queue = append(queue, next.ID)
		}
	}
	inputs := make([]sdk.VersionInput, 0, len(known))
	for _, p := range known {
		inputs = append(inputs, verInput(p))
	}
	graph := sdk.BuildVersionGraph(start.ID, inputs, sdk.Limits{MaxNodes: verMaxNodes, MaxDepth: verMaxDepth})
	if walkTruncated {
		oldest := chain[len(chain)-1]
		graph.Edges = slices.DeleteFunc(graph.Edges, func(edge sdk.VersionEdge) bool {
			return edge.From == oldest && edge.State == sdk.EdgeUnresolved
		})
	}
	selected, ok := known[graph.Selected]
	if !ok {
		selected = start
	}
	history.Selected = selected
	history.Rule = verRules[graph.SelectedBy]
	if graph.Forked {
		history.Leaves = len(graph.Leaves)
	}
	history.Tree = verTree(graph, known, a.now())
	history.Truncated = walkTruncated || budgetReached || graph.Truncated
	history.Shown = len(graph.Nodes)
	identity, found, err := a.identity(w, r)
	if err != nil {
		return verHistory{}, http.StatusInternalServerError
	}
	if found && identity.DID == start.AuthorID {
		history.PublishURL = "/compose?supersedes=" + url.QueryEscape(selected.ID)
	}
	return history, http.StatusOK
}

// verVerified is true for a network record that passed verification; demo posts and unverifiable records have no version chain.
func verVerified(p post) bool { return p.Class != "" && p.Unverifiable == "" }

// verLookupGaps names the sources that failed or could not answer a lookup for later versions, so an incomplete search is reported.
func verLookupGaps(coverage Coverage) []string {
	var lines []string
	if coverage.Note != "" {
		lines = append(lines, "Later versions: "+coverage.Note)
	}
	for _, source := range coverage.Sources {
		if !source.OK {
			lines = append(lines, "Later versions: "+source.Label+": "+source.Text)
		}
	}
	return lines
}

// verInput gives the version graph the verified message when the post has one, so
// the graph ranks leaves by the message's own signer. A post without one (demo data)
// is rebuilt from its fields.
func verInput(p post) sdk.VersionInput {
	if p.Verified != nil {
		return sdk.VersionInput{Message: p.Verified.Message, IssuedAt: p.IssuedAt}
	}
	m := wire.PostMessage{MessageID: p.ID, AuthorID: p.AuthorID, Timestamp: p.Timestamp, Payload: wire.PostPayload{ReplyTo: p.ReplyTo, Supersedes: p.Supersedes}}
	if !p.RootSigned {
		m.SignerID = "delegate"
	}
	return sdk.VersionInput{Message: m, IssuedAt: p.IssuedAt}
}

// verTree nests the verified versions under the earlier version each one supersedes. Links that are unresolved or rejected are shown where they occur, and every version stays listed.
func verTree(graph sdk.VersionGraph, known map[string]post, now time.Time) []verNode {
	parent := make(map[string]string)
	missing := make(map[string]string)
	rejected := make(map[string]string)
	for _, edge := range graph.Edges {
		switch edge.State {
		case sdk.EdgeVerified:
			parent[edge.From] = edge.To
		case sdk.EdgeUnresolved:
			missing[edge.From] = edge.To
		case sdk.EdgeRejected:
			rejected[edge.From] = edge.Reason
		}
	}
	children := make(map[string][]string)
	var roots []string
	for _, id := range graph.Nodes {
		if _, ok := known[id]; !ok {
			continue
		}
		if earlier, ok := parent[id]; ok {
			children[earlier] = append(children[earlier], id)
		} else {
			roots = append(roots, id)
		}
	}
	var build func(id string, branch bool) verNode
	build = func(id string, branch bool) verNode {
		p := known[id]
		node := verNode{
			ID: id, Short: verShort(id), When: relativeTime(p.IssuedAt, now), Signer: "delegate", FirstLine: verFirstLine(p.Text), Post: p,
			Selected: id == graph.Selected, Leaf: slices.Contains(graph.Leaves, id), Branch: branch, Missing: missing[id],
		}
		if p.RootSigned {
			node.Signer = "author key"
		}
		if reason, ok := rejected[id]; ok {
			node.Rejected = verRejectedText(reason)
		}
		kids := verOrdered(children[id], known)
		if len(kids) > 1 {
			node.ChildLabel = "Branch from " + verShort(id)
		}
		for _, kid := range kids {
			node.Children = append(node.Children, build(kid, len(kids) > 1))
		}
		return node
	}
	roots = verOrdered(roots, known)
	tree := make([]verNode, 0, len(roots))
	for _, root := range roots {
		tree = append(tree, build(root, false))
	}
	return tree
}

// verOrdered sorts version IDs by receipt time, then by ID, so the same records always read in the same order.
func verOrdered(ids []string, known map[string]post) []string {
	slices.SortFunc(ids, func(a, b string) int {
		if c := known[a].IssuedAt.Compare(known[b].IssuedAt); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return ids
}

func verRejectedText(reason string) string {
	switch reason {
	case "author_mismatch":
		return "Not a valid version link: different author"
	case "reply_to_mismatch":
		return "Not a valid version link: different reply target"
	}
	return "Not a valid version link"
}

func verShort(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:12] + "…"
}

func verFirstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func (a *App) versionsHandler(w http.ResponseWriter, r *http.Request) {
	history, status := a.versionHistory(w, r, r.PathValue("id"))
	switch status {
	case http.StatusOK:
		a.page(w, r, "versions-page", versionsPage{Layout: a.layout(r, "home", "Version history"), Versions: history})
	case http.StatusNotFound:
		a.writeDetailNotFound(w, r)
	default:
		a.fail(w, r, status, "Version history could not be read.")
	}
}

// versionsFragmentHandler returns only the graph, for a control on the post page; the full page is the fallback.
func (a *App) versionsFragmentHandler(w http.ResponseWriter, r *http.Request) {
	history, status := a.versionHistory(w, r, r.PathValue("id"))
	switch status {
	case http.StatusOK:
		a.fragment(w, r, "versions-graph", history)
	case http.StatusNotFound:
		a.fail(w, r, status, "Post not found.")
	default:
		a.fail(w, r, status, "Version history could not be read.")
	}
}
