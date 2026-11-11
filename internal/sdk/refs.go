package sdk

import (
	"regexp"
	"sort"
	"strings"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/wire"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var asReferenceIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// RefKind identifies the target kind of a declared reference.
type RefKind int

const (
	// RefMessage identifies a fy1 message target.
	RefMessage RefKind = iota
	// RefAuthor identifies a did:key author target.
	RefAuthor
)

// ResolveState describes a caller's later resolution attempt; planning performs no resolution.
type ResolveState int

const (
	// RefUnresolved means the caller has not attempted resolution.
	RefUnresolved ResolveState = iota
	// RefResolved means the caller verified a target.
	RefResolved
	// RefUnavailable means the target could not be fetched.
	RefUnavailable
	// RefInvalid means the target failed verification.
	RefInvalid
)

// PlannedRef carries an untrusted label; Target, never Label, identifies the referent.
type PlannedRef struct {
	ID, Target string
	Kind       RefKind
	Used       bool
}

// ReferencePlan lists signed declarations and active text references without resolving them.
type ReferencePlan struct {
	Refs    []PlannedRef
	Missing []string
}

// PlanReferences plans signed refs and active Markdown links; labels remain untrusted and target, never label, identifies the referent.
func PlanReferences(p wire.PostPayload) (ReferencePlan, error) {
	plan := ReferencePlan{Refs: []PlannedRef{}}
	refs := []wire.PostReference{}
	if p.Refs != nil {
		refs = *p.Refs
	}
	if err := content.ValidateRefs(refs); err != nil {
		return ReferencePlan{}, err
	}
	byID := make(map[string]int, len(refs))
	for _, r := range refs {
		kind := RefMessage
		if strings.HasPrefix(r.Target, "did:key:") {
			kind = RefAuthor
		}
		byID[r.ID] = len(plan.Refs)
		plan.Refs = append(plan.Refs, PlannedRef{ID: r.ID, Target: r.Target, Kind: kind})
	}
	rendered, err := content.Render(p, content.RenderOptions{})
	if err != nil {
		return ReferencePlan{}, err
	}
	for _, link := range rendered.Links {
		if link.Kind == content.LinkRef {
			if i, ok := byID[link.RefID]; ok {
				plan.Refs[i].Used = true
			}
		}
	}
	declared := make(map[string]bool, len(byID))
	for id := range byID {
		declared[id] = true
	}
	missing := make(map[string]bool)
	if p.ContentFormat == content.FormatMarkdown {
		document := goldmark.New().Parser().Parse(text.NewReader([]byte(p.Text)))
		_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			link, ok := node.(*ast.Link)
			if !entering || !ok || link.Reference != nil || link.Title != nil {
				return ast.WalkContinue, nil
			}
			destination := string(link.Destination)
			if !strings.HasPrefix(destination, "fy-ref:") {
				return ast.WalkContinue, nil
			}
			id := strings.TrimPrefix(destination, "fy-ref:")
			if asReferenceIDPattern.MatchString(id) && !declared[id] && !missing[id] {
				missing[id] = true
				plan.Missing = append(plan.Missing, id)
			}
			return ast.WalkContinue, nil
		})
	}
	sort.Strings(plan.Missing)
	return plan, nil
}
