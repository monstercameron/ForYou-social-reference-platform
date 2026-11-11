package discovery

import (
	"bytes"
	"strconv"
)

// Candidate is one examined sequence for a /v1/changes response.
type Candidate struct {
	Sequence uint64
	Bytes    []byte
	Visible  bool
}

// EncodeChangesPage writes a ChangesPage with exact nested message bytes.
// providerID must be a did:key identifier, whose characters need no JSON escaping.
func EncodeChangesPage(providerID string, items []Candidate, nextAfter uint64, more bool) []byte {
	var output bytes.Buffer
	output.WriteString(`{"items":[`)
	for i, item := range items {
		if i > 0 {
			output.WriteByte(',')
		}
		output.WriteString(`{"message":`)
		output.Write(item.Bytes)
		output.WriteString(`,"sequence":`)
		output.WriteString(strconv.FormatUint(item.Sequence, 10))
		output.WriteString("}")
	}
	output.WriteString(`],"more":`)
	if more {
		output.WriteString("true")
	} else {
		output.WriteString("false")
	}
	output.WriteString(`,"next_after":`)
	output.WriteString(strconv.FormatUint(nextAfter, 10))
	output.WriteString(`,"provider_id":"`)
	output.WriteString(providerID)
	output.WriteString(`"}`)
	return output.Bytes()
}

// BuildChanges selects visible candidates within the item and page-byte limits.
func BuildChanges(providerID string, after uint64, limit int, maxAssigned uint64, candidates []Candidate, complete bool) []byte {
	if limit < 1 || limit > maxMessagesPerPage {
		return EncodeChangesPage(providerID, nil, after, maxAssigned > after)
	}
	items := make([]Candidate, 0, limit)
	covered := after
	consumed := 0
	reservedNextAfter := after
	if maxAssigned > reservedNextAfter {
		reservedNextAfter = maxAssigned
	}
	pageBytes := len(EncodeChangesPage(providerID, nil, reservedNextAfter, false))
	for _, candidate := range candidates {
		if len(items) >= limit {
			break
		}
		if candidate.Visible {
			itemBytes := len(`{"message":`) + len(candidate.Bytes) + len(`,"sequence":`) + len(strconv.FormatUint(candidate.Sequence, 10)) + 1
			if len(items) > 0 {
				itemBytes++
			}
			if pageBytes+itemBytes > MaxPageBytes {
				break
			}
			items = append(items, candidate)
			pageBytes += itemBytes
		}
		covered = candidate.Sequence
		consumed++
	}
	if consumed == len(candidates) && complete {
		if maxAssigned > covered {
			covered = maxAssigned
		}
	}
	if covered < after {
		covered = after
	}
	return EncodeChangesPage(providerID, items, covered, maxAssigned > covered)
}
