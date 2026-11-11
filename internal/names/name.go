package names

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ErrInvalidQuery identifies an invalid name lookup query.
var ErrInvalidQuery = errors.New("names: invalid lookup query")

// ValidateName checks a signed display name without changing it.
func ValidateName(s string) error {
	if !utf8.ValidString(s) || !norm.NFC.IsNormalString(s) || len(s) > MaxNameBytes {
		return ErrInvalidName
	}
	count := 0
	var previousSpace bool
	var first rune
	for _, r := range s {
		count++
		if count == 1 {
			first = r
		}
		if unicode.Is(ucd15Excluded, r) {
			return ErrInvalidName
		}
		space := r == ' '
		if previousSpace && space {
			return ErrInvalidName
		}
		previousSpace = space
	}
	if count < 1 || count > MaxNameScalars {
		return ErrInvalidName
	}
	last, _ := utf8.DecodeLastRuneInString(s)
	if unicode.Is(unicode.White_Space, first) || unicode.Is(unicode.White_Space, last) {
		return ErrInvalidName
	}
	return nil
}

// NormalizeName applies NFC to user input and then validates it for signing.
func NormalizeName(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", ErrInvalidName
	}
	normalized := norm.NFC.String(s)
	if err := ValidateName(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

// CheckQuery validates lookup query length and returns its NFC form.
func CheckQuery(q string) (string, error) {
	if !utf8.ValidString(q) {
		return "", ErrInvalidQuery
	}
	normalized := norm.NFC.String(q)
	if len(normalized) > MaxNameBytes || utf8.RuneCountInString(normalized) < 1 || utf8.RuneCountInString(normalized) > MaxNameScalars {
		return "", ErrInvalidQuery
	}
	return normalized, nil
}

// FoldKey returns the NFC and Unicode simple case-folded lookup key.
func FoldKey(s string) string {
	// NFC and simple folding of characters assigned in Unicode 15.0.0 are stable; later characters fold only to themselves.
	var folded strings.Builder
	for _, r := range norm.NFC.String(s) {
		if unicode.Is(ucd15Excluded, r) {
			folded.WriteRune(r)
			continue
		}
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if !unicode.Is(ucd15Excluded, next) && next < minimum {
				minimum = next
			}
		}
		folded.WriteRune(minimum)
	}
	return folded.String()
}

// SameName reports whether two names match under NFC and Unicode simple folding.
func SameName(a, b string) bool { return FoldKey(a) == FoldKey(b) }

// Fingerprint returns the profile's short fingerprint for an author DID.
func Fingerprint(authorID string) string {
	digest := sha256.Sum256([]byte(authorID))
	encoded := hex.EncodeToString(digest[:])[:16]
	return encoded[:4] + " " + encoded[4:8] + " " + encoded[8:12] + " " + encoded[12:]
}
