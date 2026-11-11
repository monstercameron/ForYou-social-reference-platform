package content

import (
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// MaxMediaSlots is the maximum number of external media slots in a post.
const MaxMediaSlots = 16

// MaxRefs is the maximum number of signed structured references in a post.
const MaxRefs = 32

// MaxAltTextRunes is the maximum number of Unicode scalar values in alt text.
const MaxAltTextRunes = 2000

var (
	// ErrInvalidMediaSlot identifies invalid media slot metadata.
	ErrInvalidMediaSlot = errors.New("content: invalid media slot")
	// ErrInvalidRef identifies invalid signed reference metadata.
	ErrInvalidRef = errors.New("content: invalid reference")
	// ErrInvalidPostContent identifies invalid post content fields.
	ErrInvalidPostContent = errors.New("content: invalid post content")
	validIDPattern        = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	validHexDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	validMessageIDPattern = regexp.MustCompile(`^fy1:[0-9a-f]{64}$`)
)

// ValidateMediaSlots checks media slot metadata without fetching media.
func ValidateMediaSlots(slots []wire.MediaReference) error {
	if len(slots) > MaxMediaSlots {
		return fmt.Errorf("%w: %d slots exceed maximum of %d", ErrInvalidMediaSlot, len(slots), MaxMediaSlots)
	}
	ids := make(map[string]struct{}, len(slots))
	for index, slot := range slots {
		invalid := func(rule string) error {
			return fmt.Errorf("%w: slot index %d: %s", ErrInvalidMediaSlot, index, rule)
		}
		if !validIDPattern.MatchString(slot.ID) {
			return invalid("id must match [a-z0-9_-]{1,32}")
		}
		if _, exists := ids[slot.ID]; exists {
			return invalid("id is duplicated")
		}
		ids[slot.ID] = struct{}{}
		if slot.Kind != "image" && slot.Kind != "video" {
			return invalid("kind must be image or video")
		}
		if _, err := ParseExternalURL(slot.URL); err != nil {
			return fmt.Errorf("%w: slot index %d: url: %w", ErrInvalidMediaSlot, index, err)
		}
		if !validHexDigestPattern.MatchString(slot.SHA256) {
			return invalid("sha256 must be 64 lowercase hexadecimal digits")
		}
		if slot.SizeBytes < 0 || slot.SizeBytes > 9007199254740991 {
			return invalid("size_bytes must be from 1 to 2^53-1 when present")
		}
		if slot.AltText != nil && (!utf8.ValidString(*slot.AltText) || utf8.RuneCountInString(*slot.AltText) > MaxAltTextRunes) {
			return invalid("alt_text must be valid UTF-8 of at most 2000 Unicode scalar values")
		}
	}
	return nil
}

// ValidateRefs checks signed reference metadata without resolving any target.
func ValidateRefs(refs []wire.PostReference) error {
	if len(refs) > MaxRefs {
		return fmt.Errorf("%w: %d refs exceed maximum of %d", ErrInvalidRef, len(refs), MaxRefs)
	}
	ids := make(map[string]struct{}, len(refs))
	targets := make(map[string]struct{}, len(refs))
	for index, ref := range refs {
		invalid := func(rule string) error {
			return fmt.Errorf("%w: reference index %d: %s", ErrInvalidRef, index, rule)
		}
		if !validIDPattern.MatchString(ref.ID) {
			return invalid("id must match [a-z0-9_-]{1,32}")
		}
		if _, exists := ids[ref.ID]; exists {
			return invalid("id is duplicated")
		}
		ids[ref.ID] = struct{}{}
		if _, exists := targets[ref.Target]; exists {
			return invalid("target is duplicated")
		}
		if !validMessageIDPattern.MatchString(ref.Target) {
			if _, err := keys.ParseDID(ref.Target); err != nil {
				return invalid("target must be a canonical fy1 message ID or Ed25519 did:key")
			}
		}
		targets[ref.Target] = struct{}{}
	}
	return nil
}

// ValidatePostContent checks the semantic fields of a post payload.
func ValidatePostContent(p wire.PostPayload) error {
	if p.ContentFormat != "text/plain" && p.ContentFormat != "text/markdown" {
		return fmt.Errorf("%w: content_format must be text/plain or text/markdown", ErrInvalidPostContent)
	}
	var slots []wire.MediaReference
	if p.MediaSlots != nil {
		slots = *p.MediaSlots
	}
	if p.Text == "" && len(slots) == 0 {
		return fmt.Errorf("%w: text may be empty only when media slots are present", ErrInvalidPostContent)
	}
	if err := ValidateMediaSlots(slots); err != nil {
		return err
	}
	var refs []wire.PostReference
	if p.Refs != nil {
		refs = *p.Refs
	}
	if err := ValidateRefs(refs); err != nil {
		return err
	}
	return nil
}
