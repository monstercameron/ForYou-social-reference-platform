package discovery

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/keys"
)

// Route identifies a public listing or index route.
type Route int

const (
	// Messages lists all matching messages.
	Messages Route = iota + 1
	// AuthorMessages lists messages by one author.
	AuthorMessages
	// Replies lists messages that reply to a target message.
	Replies
	// References lists messages that declare a target reference.
	References
	// Versions lists messages that supersede a target message.
	Versions
)

const (
	// DefaultLimit is the default number of records in a page.
	DefaultLimit = 50
	// MaxLimit is the greatest accepted number of records in a page.
	MaxLimit = 100
	// MaxRawQueryBytes bounds a raw listing query.
	MaxRawQueryBytes = 16384
	// MaxAfter is the greatest safe-integer changes offset.
	MaxAfter = 1<<53 - 1
)

// Error is a 400 validation failure carrying its PublicReadError status name.
type Error struct{ Status string }

// Error returns the public status name.
func (e *Error) Error() string { return e.Status }

var (
	// ErrInvalidQuery identifies a malformed listing or changes query.
	ErrInvalidQuery = &Error{Status: "invalid_query"}
	// ErrInvalidCursor identifies a cursor with an invalid encoding.
	ErrInvalidCursor = &Error{Status: "invalid_cursor"}
)

// Query is a validated, normalized listing request without its cursor.
type Query struct {
	Route    Route
	Target   string
	AuthorID string
	From     time.Time
	Until    time.Time
	Limit    int
	fromSet  bool
	untilSet bool
}

// ParseListQuery validates a listing query in raw encoding order and normalizes its filters.
func ParseListQuery(route Route, target, rawQuery string) (q Query, cursor string, err error) {
	if !validRoute(route) || !validRouteTarget(route, target) {
		return q, "", ErrInvalidQuery
	}
	values, err := parseRawQuery(rawQuery, route == AuthorMessages, "date", "from", "until", "limit", "cursor", "author_id")
	if err != nil {
		return q, "", ErrInvalidQuery
	}
	q = Query{Route: route, Target: target, Limit: DefaultLimit}
	if raw, ok := values["limit"]; ok {
		limit, parseErr := parseLimit(raw)
		if parseErr != nil {
			return Query{}, "", ErrInvalidQuery
		}
		q.Limit = limit
	}
	if route == AuthorMessages {
		author := values["author_id"]
		if author == "" {
			return Query{}, "", ErrInvalidQuery
		}
		if _, parseErr := keys.ParseDID(author); parseErr != nil {
			return Query{}, "", ErrInvalidQuery
		}
		q.AuthorID = author
	}
	if date, ok := values["date"]; ok {
		if _, hasFrom := values["from"]; hasFrom {
			return Query{}, "", ErrInvalidQuery
		}
		if _, hasUntil := values["until"]; hasUntil {
			return Query{}, "", ErrInvalidQuery
		}
		day, parseErr := parseDate(date)
		if parseErr != nil || date == "9999-12-31" {
			return Query{}, "", ErrInvalidQuery
		}
		q.From = day
		q.Until = day.AddDate(0, 0, 1)
		q.fromSet = true
		q.untilSet = true
	} else {
		if raw, ok := values["from"]; ok {
			q.From, err = parseTimestamp(raw)
			if err != nil {
				return Query{}, "", ErrInvalidQuery
			}
			q.fromSet = true
		}
		if raw, ok := values["until"]; ok {
			q.Until, err = parseTimestamp(raw)
			if err != nil {
				return Query{}, "", ErrInvalidQuery
			}
			q.untilSet = true
		}
		if q.fromSet && q.untilSet && !q.From.Before(q.Until) {
			return Query{}, "", ErrInvalidQuery
		}
	}
	if raw, ok := values["cursor"]; ok {
		if !validCursor(raw) {
			return Query{}, "", ErrInvalidCursor
		}
		cursor = raw
	}
	return q, cursor, nil
}

// ParseChangesQuery validates the incremental-sync query.
func ParseChangesQuery(rawQuery string) (after uint64, limit int, err error) {
	values, err := parseRawQuery(rawQuery, false, "after", "limit")
	if err != nil {
		return 0, 0, ErrInvalidQuery
	}
	limit = DefaultLimit
	if raw, ok := values["limit"]; ok {
		value, parseErr := parseLimit(raw)
		if parseErr != nil {
			return 0, 0, ErrInvalidQuery
		}
		limit = value
	}
	if raw, ok := values["after"]; ok {
		if raw != "0" && (raw == "" || raw[0] < '1' || raw[0] > '9' || !digits(raw)) {
			return 0, 0, ErrInvalidQuery
		}
		value, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil || value > MaxAfter {
			return 0, 0, ErrInvalidQuery
		}
		after = value
	}
	return after, limit, nil
}

// ParseTarget validates and decodes an escaped index-route path target.
func ParseTarget(route Route, escaped string) (string, error) {
	if route != Replies && route != References && route != Versions {
		return "", ErrInvalidQuery
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil || percentEncode(decoded) != escaped {
		return "", ErrInvalidQuery
	}
	if isMessageID(decoded) {
		return decoded, nil
	}
	if route == References && strings.HasPrefix(decoded, "did:key:z6Mk") {
		if _, err := keys.ParseDID(decoded); err == nil {
			return decoded, nil
		}
	}
	return "", ErrInvalidQuery
}

// Binding returns the deterministic cursor binding for this normalized query.
func (q Query) Binding() string {
	var parts []string
	parts = append(parts, strconv.Itoa(int(q.Route)), q.Target, q.AuthorID, boundTime(q.From, q.hasFrom()), boundTime(q.Until, q.hasUntil()), strconv.Itoa(q.Limit))
	var result strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&result, "%d:%s", len(part), part)
	}
	return result.String()
}

// Match reports whether a record satisfies this query's time and route predicates.
func (q Query) Match(r Record) bool {
	if q.hasFrom() && r.Timestamp.Before(q.From) || q.hasUntil() && !r.Timestamp.Before(q.Until) {
		return false
	}
	switch q.Route {
	case Messages:
		return true
	case AuthorMessages:
		return r.AuthorID == q.AuthorID
	case Replies:
		return r.ReplyTo == q.Target
	case References:
		for _, target := range r.RefTargets {
			if target == q.Target {
				return true
			}
		}
		return false
	case Versions:
		return r.Supersedes == q.Target
	default:
		return false
	}
}

func parseRawQuery(raw string, allowAuthor bool, allowed ...string) (map[string]string, error) {
	if len(raw) > MaxRawQueryBytes || !isASCII(raw) {
		return nil, ErrInvalidQuery
	}
	allowedNames := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		if name != "author_id" || allowAuthor {
			allowedNames[name] = true
		}
	}
	values := make(map[string]string)
	if raw == "" {
		return values, nil
	}
	for _, component := range strings.Split(raw, "&") {
		if component == "" {
			return nil, ErrInvalidQuery
		}
		key, value, found := strings.Cut(component, "=")
		if !found || !allowedNames[key] {
			return nil, ErrInvalidQuery
		}
		if _, exists := values[key]; exists {
			return nil, ErrInvalidQuery
		}
		decoded, err := decodeCanonicalValue(value)
		if err != nil || decoded == "" && key != "cursor" {
			return nil, ErrInvalidQuery
		}
		values[key] = decoded
	}
	return values, nil
}

func decodeCanonicalValue(raw string) (string, error) {
	decoded, err := url.QueryUnescape(strings.ReplaceAll(raw, "+", "%2B"))
	if err != nil || !utf8.ValidString(decoded) || percentEncode(decoded) != raw {
		return "", ErrInvalidQuery
	}
	return decoded, nil
}

func percentEncode(value string) string {
	const hex = "0123456789ABCDEF"
	var output strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if isUnreserved(b) {
			output.WriteByte(b)
		} else {
			output.WriteByte('%')
			output.WriteByte(hex[b>>4])
			output.WriteByte(hex[b&15])
		}
	}
	return output.String()
}

func parseDate(value string) (time.Time, error) {
	if len(value) != len("2000-01-01") {
		return time.Time{}, ErrInvalidQuery
	}
	day, err := time.Parse("2006-01-02", value)
	if err != nil || day.Year() == 0 || day.Format("2006-01-02") != value {
		return time.Time{}, ErrInvalidQuery
	}
	return day.UTC(), nil
}

func parseTimestamp(value string) (time.Time, error) {
	const layout = "2006-01-02T15:04:05Z"
	if len(value) != len("2000-01-01T00:00:00Z") {
		return time.Time{}, ErrInvalidQuery
	}
	timestamp, err := time.Parse(layout, value)
	if err != nil || timestamp.Year() == 0 || timestamp.Format(layout) != value {
		return time.Time{}, ErrInvalidQuery
	}
	return timestamp.UTC(), nil
}

func validCursor(value string) bool {
	if len(value) != 43 {
		return false
	}
	for i := range value {
		b := value[i]
		if !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-') {
			return false
		}
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validRoute(route Route) bool { return route >= Messages && route <= Versions }

func validRouteTarget(route Route, target string) bool {
	switch route {
	case Messages, AuthorMessages:
		return target == ""
	case Replies, Versions:
		return isMessageID(target)
	case References:
		if isMessageID(target) {
			return true
		}
		_, err := keys.ParseDID(target)
		return err == nil
	default:
		return false
	}
}

func isMessageID(value string) bool {
	if len(value) != 68 || !strings.HasPrefix(value, "fy1:") {
		return false
	}
	for i := 4; i < len(value); i++ {
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}

func boundTime(value time.Time, present bool) string {
	if !present {
		return "-"
	}
	return "+" + value.UTC().Format("2006-01-02T15:04:05Z")
}

func (q Query) hasFrom() bool  { return q.fromSet || !q.From.IsZero() }
func (q Query) hasUntil() bool { return q.untilSet || !q.Until.IsZero() }

func digits(value string) bool {
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func parseLimit(raw string) (int, error) {
	if len(raw) > 3 || raw == "" || raw[0] < '1' || raw[0] > '9' || !digits(raw) {
		return 0, ErrInvalidQuery
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit > MaxLimit {
		return 0, ErrInvalidQuery
	}
	return limit, nil
}

func isASCII(value string) bool {
	for i := range value {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isUnreserved(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.' || b == '_' || b == '~'
}
