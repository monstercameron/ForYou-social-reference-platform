package message

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// EdgeStatus describes whether a post is an original, has an unresolved
// supersedes reference, validly updates its target, or has an invalid edge.
type EdgeStatus int

const (
	// Original means the post has no supersedes reference.
	Original EdgeStatus = iota
	// Unresolved means the referenced target is not available and verified here.
	Unresolved
	// ValidEdge means the post validly updates the verified target.
	ValidEdge
	// InvalidEdge means the supersedes reference cannot form an update edge.
	InvalidEdge
)

// String returns the edge status name.
func (s EdgeStatus) String() string {
	switch s {
	case Original:
		return "original"
	case Unresolved:
		return "unresolved"
	case ValidEdge:
		return "valid"
	case InvalidEdge:
		return "invalid"
	default:
		return "unknown"
	}
}

// Edge classifies update's supersedes reference against a fully verified target.
// Invalid and unresolved edges affect only the claimed update relation, not either post.
func Edge(update wire.PostMessage, target *wire.PostMessage) EdgeStatus {
	supersedes := update.Payload.Supersedes
	if supersedes == "" {
		return Original
	}
	if supersedes == update.MessageID {
		return InvalidEdge
	}
	if target == nil || target.MessageID != supersedes {
		return Unresolved
	}
	if target.AuthorID != update.AuthorID || !SameReplyTo(update.Payload, target.Payload) {
		return InvalidEdge
	}
	return ValidEdge
}

// CheckAdmissionEdge checks update edge constraints that can be established before payment.
// Unknown targets remain admissible and leave the edge unresolved.
func CheckAdmissionEdge(i wire.Intent, id string, target *wire.PostMessage) error {
	supersedes := i.Payload.Supersedes
	if supersedes == "" {
		return nil
	}
	if supersedes == id {
		return fmt.Errorf("%w: self-reference", ErrInvalidEdge)
	}
	if target != nil && target.MessageID == supersedes &&
		(target.AuthorID != i.AuthorID || !SameReplyTo(i.Payload, target.Payload)) {
		return fmt.Errorf("%w: verified target has a mismatched author or reply_to", ErrInvalidEdge)
	}
	return nil
}

// SameReplyTo reports whether two posts have the same thread parent, including absence.
func SameReplyTo(a, b wire.PostPayload) bool {
	return a.ReplyTo == b.ReplyTo
}

// RootSigned reports whether m is signed directly by its author without delegation.
func RootSigned(m wire.PostMessage) bool {
	return (m.SignerID == "" || m.SignerID == m.AuthorID) && m.Delegation == nil
}

// SelectLeaf selects a local display default among the known terminal leaves.
// This does not establish consensus, prove time or authorial intent, or claim global completeness.
func SelectLeaf(leaves []wire.PostMessage) (wire.PostMessage, error) {
	if len(leaves) == 0 {
		return wire.PostMessage{}, errors.New("cannot select from an empty leaf set")
	}
	type versionCandidate struct {
		post       wire.PostMessage
		issuedAt   time.Time
		rootSigned bool
	}
	candidates := make([]versionCandidate, len(leaves))
	for index, leaf := range leaves {
		issuedAt, err := wire.ParseTime(leaf.SubmissionReceipt.IssuedAt)
		if err != nil {
			return wire.PostMessage{}, fmt.Errorf("parse receipt issued_at: %w", err)
		}
		candidates[index] = versionCandidate{post: leaf, issuedAt: issuedAt, rootSigned: RootSigned(leaf)}
	}
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if versionLeafPreferred(candidate.rootSigned, candidate.issuedAt, candidate.post.MessageID,
			selected.rootSigned, selected.issuedAt, selected.post.MessageID) {
			selected = candidate
		}
	}
	return selected.post, nil
}

func versionLeafPreferred(rootSigned bool, issuedAt time.Time, messageID string, selectedRootSigned bool, selectedIssuedAt time.Time, selectedMessageID string) bool {
	if rootSigned != selectedRootSigned {
		return rootSigned
	}
	if !issuedAt.Equal(selectedIssuedAt) {
		return issuedAt.After(selectedIssuedAt)
	}
	return messageID < selectedMessageID
}

// Limits bounds work while resolving a locally known version component.
type Limits struct {
	// MaxNodes is the maximum number of records in the component; exceeding it returns ErrWorkLimit without a partial result.
	MaxNodes int
	// MaxDepth is the maximum number of supersedes edges on the longest root path; exceeding it returns ErrWorkLimit without a partial result.
	MaxDepth int
	// MaxFanout is the maximum number of valid children of one record; exceeding it returns ErrWorkLimit without a partial result.
	MaxFanout int
}

// DefaultLimits is a local work budget; the specification does not prescribe these values.
var DefaultLimits = Limits{MaxNodes: 10000, MaxDepth: 1000, MaxFanout: 1000}

// Chain is a locally resolved component over records known to the caller. Its head is a
// local display default, not consensus, proof of time or authorial intent, or a claim of
// global completeness.
type Chain struct {
	// Root is the root message ID supplied to Resolve.
	Root string
	// Head is the SelectLeaf choice among the known terminal leaves.
	Head wire.PostMessage
	// Leaves contains every known terminal leaf ID, sorted in ASCII order.
	Leaves []string
	// Members contains every known component ID, including the root, sorted in ASCII order.
	Members []string
	// Forked reports whether the component has more than one known leaf.
	Forked bool
}

var (
	// ErrInvalidEdge is the public invalid_message error for a rejected edge.
	ErrInvalidEdge error = apierr.New(apierr.InvalidMessage, errors.New("invalid supersedes edge"))
	// ErrCycle indicates repeated traversal of an ID in a version component.
	ErrCycle = errors.New("version graph contains a cycle")
	// ErrWorkLimit indicates resolution exceeded its local work budget.
	ErrWorkLimit = errors.New("version graph work limit exceeded")
	// ErrIDCollision indicates distinct message intents claim the same ID.
	ErrIDCollision = errors.New("distinct intents claim the same message_id")
)

// Resolve builds the verified component rooted at root from records the caller knows.
// The selected head is a local display default, not consensus, proof of time or intent,
// or a claim of global completeness. The function verifies no signatures or trust proofs.
func Resolve(root wire.PostMessage, known []wire.PostMessage, lim Limits) (Chain, error) {
	byID := make(map[string]wire.PostMessage, len(known)+1)
	byID[root.MessageID] = root
	for _, record := range known {
		if existing, ok := byID[record.MessageID]; ok {
			existingIntent, err := canon.EncodeWithout(existing, "message_id", "signature", "submission_receipt")
			if err != nil {
				return Chain{}, fmt.Errorf("encode known intent: %w", err)
			}
			intent, err := canon.EncodeWithout(record, "message_id", "signature", "submission_receipt")
			if err != nil {
				return Chain{}, fmt.Errorf("encode known intent: %w", err)
			}
			if string(existingIntent) != string(intent) {
				return Chain{}, ErrIDCollision
			}
			continue
		}
		byID[record.MessageID] = record
	}
	children := make(map[string][]string)
	for id, record := range byID {
		if record.Payload.Supersedes == "" {
			continue
		}
		parent, ok := byID[record.Payload.Supersedes]
		if ok && Edge(record, &parent) == ValidEdge {
			children[parent.MessageID] = append(children[parent.MessageID], id)
		}
	}

	type versionVisit struct {
		id    string
		depth int
	}
	stack := []versionVisit{{id: root.MessageID, depth: 0}}
	seen := make(map[string]struct{})
	leafMessages := make([]wire.PostMessage, 0)
	chain := Chain{Root: root.MessageID}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[current.id]; ok {
			return Chain{}, ErrCycle
		}
		seen[current.id] = struct{}{}
		if len(seen) > lim.MaxNodes || current.depth > lim.MaxDepth {
			return Chain{}, ErrWorkLimit
		}
		next := children[current.id]
		if len(next) > lim.MaxFanout {
			return Chain{}, ErrWorkLimit
		}
		chain.Members = append(chain.Members, current.id)
		if len(next) == 0 {
			leafMessages = append(leafMessages, byID[current.id])
			continue
		}
		sort.Strings(next)
		for _, child := range next {
			stack = append(stack, versionVisit{id: child, depth: current.depth + 1})
		}
	}
	for _, leaf := range leafMessages {
		chain.Leaves = append(chain.Leaves, leaf.MessageID)
	}
	sort.Strings(chain.Leaves)
	sort.Strings(chain.Members)
	head, err := SelectLeaf(leafMessages)
	if err != nil {
		return Chain{}, err
	}
	chain.Head = head
	chain.Forked = len(chain.Leaves) > 1
	return chain, nil
}
