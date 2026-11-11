package names

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const (
	// IDPrefix is the display name record ID prefix.
	IDPrefix = "fynr2:"
	// MaxNameScalars is the maximum scalar count in a display name or query.
	MaxNameScalars = 64
	// MaxNameBytes is the maximum UTF-8 byte count in a display name or query.
	MaxNameBytes = 256
	// MaxRecordBytes is the maximum JCS record size.
	MaxRecordBytes = 4096
	// MaxAvatarURLLength is the maximum avatar URL length in ASCII characters.
	MaxAvatarURLLength = 2048
	// MaxAvatarBytes is the maximum number of bytes in an avatar image.
	MaxAvatarBytes = 1048576
	// MaxPageRecords is the maximum number of records in a names page.
	MaxPageRecords = 50
	// MaxPageBytes is the maximum encoded names page size.
	MaxPageBytes = MaxPageRecords*MaxRecordBytes + 1024
	// ClockTolerance is the permitted future timestamp skew.
	ClockTolerance = 60 * time.Second
)

var (
	// ErrInvalidRecord identifies an invalid or structurally malformed record.
	ErrInvalidRecord = errors.New("names: invalid record")
	// ErrInvalidName identifies a display name that does not satisfy the profile.
	ErrInvalidName = errors.New("names: invalid display name")
	// ErrIDMismatch identifies a record whose ID does not match its contents.
	ErrIDMismatch = errors.New("names: name_id does not match")
	// ErrBadSignature identifies an invalid author signature.
	ErrBadSignature = errors.New("names: bad author signature")
	// ErrFuture identifies an issued_at value outside the clock tolerance.
	ErrFuture = errors.New("names: issued_at too far in the future")
)

var nameIDPattern = regexp.MustCompile(`^fynr2:[0-9a-f]{64}$`)

// NameRecord is the signed display name of one author key in the optional profile.
type NameRecord struct {
	NameID          string  `json:"name_id"`
	AuthorID        string  `json:"author_id"`
	DisplayName     *string `json:"display_name"`
	Avatar          *Avatar `json:"avatar"`
	IssuedAt        string  `json:"issued_at"`
	PreviousNameID  *string `json:"previous_name_id"`
	AuthorSignature string  `json:"author_signature"`
}

// Avatar is an external image reference committed to by a name record.
type Avatar struct {
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// NamesPage is one page of advisory display name lookup results.
type NamesPage struct {
	Names      []NameRecord `json:"names"`
	NextCursor *string      `json:"next_cursor"`
}

// ID computes the content-derived name record identifier.
func ID(r NameRecord) (string, error) {
	canonical, err := canon.EncodeWithout(r, "name_id", "author_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID(IDPrefix, "name-record-id", canonical)
}

// Build creates a root-signed name record. A delegate key must never sign a
// name record; posting delegation grants no authority over names.
func Build(root keys.PrivateKey, displayName *string, avatar *Avatar, issuedAt time.Time, previousNameID *string) (NameRecord, error) {
	if displayName != nil && ValidateName(*displayName) != nil {
		return NameRecord{}, ErrInvalidName
	}
	if err := ValidateAvatar(avatar, displayName); err != nil {
		return NameRecord{}, err
	}
	r := NameRecord{AuthorID: keys.DIDOf(root), DisplayName: displayName, IssuedAt: wire.FormatTime(issuedAt), PreviousNameID: previousNameID}
	if avatar != nil {
		copied := *avatar
		r.Avatar = &copied
	}
	id, err := ID(r)
	if err != nil {
		return NameRecord{}, ErrInvalidRecord
	}
	r.NameID = id
	canonical, err := canon.EncodeWithout(r, "author_signature")
	if err != nil {
		return NameRecord{}, ErrInvalidRecord
	}
	r.AuthorSignature, err = keys.Sign(root, "name-record", canonical)
	if err != nil {
		return NameRecord{}, ErrInvalidRecord
	}
	if err := Verify(r, issuedAt); err != nil {
		return NameRecord{}, fmt.Errorf("%w: built record failed verification: %v", ErrInvalidRecord, err)
	}
	return r, nil
}

// ValidateAvatar checks an avatar reference without fetching its URL. Nil is
// valid; a non-nil avatar requires a non-nil display name. Failures wrap
// ErrInvalidRecord.
func ValidateAvatar(a *Avatar, displayName *string) error {
	if a == nil {
		return nil
	}
	if displayName == nil {
		return fmt.Errorf("%w: avatar requires a display name", ErrInvalidRecord)
	}
	if len(a.URL) > MaxAvatarURLLength || content.ValidateExternalURL(a.URL) != nil {
		return fmt.Errorf("%w: invalid avatar URL", ErrInvalidRecord)
	}
	if len(a.SHA256) != 64 || strings.Trim(a.SHA256, "0123456789abcdef") != "" {
		return fmt.Errorf("%w: invalid avatar SHA-256", ErrInvalidRecord)
	}
	if a.SizeBytes < 1 || a.SizeBytes > MaxAvatarBytes {
		return fmt.Errorf("%w: invalid avatar reference", ErrInvalidRecord)
	}
	return nil
}

func value(v any, maxBytes int) (any, error) {
	encoded, err := canon.Encode(v)
	if err != nil {
		return nil, err
	}
	return canon.Parse(encoded, canon.Limits{MaxBytes: maxBytes, MaxDepth: 32})
}

func checkRecord(r NameRecord) (time.Time, error) {
	v, err := value(r, MaxRecordBytes)
	if err != nil || wire.Check("NameRecord", v) != nil {
		return time.Time{}, ErrInvalidRecord
	}
	if _, err := keys.ParseDID(r.AuthorID); err != nil {
		return time.Time{}, ErrInvalidRecord
	}
	issued, err := wire.ParseTime(r.IssuedAt)
	if err != nil {
		return time.Time{}, ErrInvalidRecord
	}
	if !nameIDPattern.MatchString(r.NameID) || r.PreviousNameID != nil && !nameIDPattern.MatchString(*r.PreviousNameID) {
		return time.Time{}, ErrInvalidRecord
	}
	if len(r.AuthorSignature) != 86 {
		return time.Time{}, ErrInvalidRecord
	}
	if _, err := keys.DecodeB64URL(r.AuthorSignature, 64); err != nil {
		return time.Time{}, ErrInvalidRecord
	}
	return issued, nil
}

// Verify checks structure, name rules, ID, root signature and future clock skew in profile order.
func Verify(r NameRecord, now time.Time) error {
	issued, err := checkRecord(r)
	if err != nil {
		return err
	}
	if r.DisplayName != nil && ValidateName(*r.DisplayName) != nil {
		return ErrInvalidName
	}
	if err := ValidateAvatar(r.Avatar, r.DisplayName); err != nil {
		return err
	}
	id, err := ID(r)
	if err != nil || id != r.NameID {
		return ErrIDMismatch
	}
	canonical, err := canon.EncodeWithout(r, "author_signature")
	if err != nil || keys.Verify(r.AuthorID, "name-record", canonical, r.AuthorSignature) != nil {
		return ErrBadSignature
	}
	if issued.After(now.Add(ClockTolerance)) {
		return ErrFuture
	}
	return nil
}

func decodeRecord(data []byte) (NameRecord, error) {
	v, err := canon.Parse(data, canon.Limits{MaxBytes: MaxRecordBytes, MaxDepth: 32})
	if err != nil || wire.Check("NameRecord", v) != nil {
		return NameRecord{}, ErrInvalidRecord
	}
	encoded, err := canon.Marshal(v)
	if err != nil {
		return NameRecord{}, ErrInvalidRecord
	}
	var r NameRecord
	if err := json.Unmarshal(encoded, &r); err != nil {
		return NameRecord{}, ErrInvalidRecord
	}
	return r, nil
}

// Parse parses a bounded record and checks its structure without verifying its signature or clock.
func Parse(data []byte) (NameRecord, error) { return decodeRecord(data) }

// Encode returns the exact JCS encoding of a structurally valid record.
func Encode(r NameRecord) ([]byte, error) {
	v, err := value(r, MaxRecordBytes)
	if err != nil || wire.Check("NameRecord", v) != nil {
		return nil, ErrInvalidRecord
	}
	return canon.Marshal(v)
}

// ParsePage parses a bounded page and checks record structure only. Callers
// must verify each record with Verify; author order and uniqueness are not checked.
func ParsePage(data []byte) (NamesPage, error) {
	v, err := canon.Parse(data, canon.Limits{MaxBytes: MaxPageBytes, MaxDepth: 32})
	if err != nil || wire.Check("NamesPage", v) != nil {
		return NamesPage{}, ErrInvalidRecord
	}
	object, ok := v.(map[string]any)
	if !ok {
		return NamesPage{}, ErrInvalidRecord
	}
	page := NamesPage{Names: make([]NameRecord, 0)}
	if object["next_cursor"] != nil {
		cursor, ok := object["next_cursor"].(string)
		if !ok || len(cursor) != 43 {
			return NamesPage{}, ErrInvalidRecord
		}
		if _, err := keys.DecodeB64URL(cursor, 32); err != nil {
			return NamesPage{}, ErrInvalidRecord
		}
		page.NextCursor = &cursor
	}
	items, ok := object["names"].([]any)
	if !ok || len(items) > MaxPageRecords {
		return NamesPage{}, ErrInvalidRecord
	}
	for _, item := range items {
		encoded, err := canon.Marshal(item)
		if err != nil {
			return NamesPage{}, ErrInvalidRecord
		}
		record, err := decodeRecord(encoded)
		if err != nil {
			return NamesPage{}, ErrInvalidRecord
		}
		page.Names = append(page.Names, record)
	}
	return page, nil
}

// EncodePage returns the exact JCS encoding of p, using an empty array for no names.
func EncodePage(p NamesPage) ([]byte, error) {
	if p.Names == nil {
		p.Names = []NameRecord{}
	}
	v, err := value(p, MaxPageBytes)
	if err != nil || wire.Check("NamesPage", v) != nil {
		return nil, ErrInvalidRecord
	}
	return canon.Marshal(v)
}
