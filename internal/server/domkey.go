package server

import "strings"

// domKey turns an identifier into a form that is safe in an element ID and in a
// CSS selector. Message IDs and DIDs contain colons, which a "#id" selector cannot
// hold, so every ID and update target built from one uses its key instead. The key
// is only an address inside the page: links and form fields keep the real identifier.
func domKey(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, id)
}
