package content

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"foryou.local/foryou/internal/wire"
)

// Media verification errors describe rejected responses, bytes, types, or budgets.
var (
	// ErrEncodedResponse indicates that the response bytes were HTTP encoded.
	ErrEncodedResponse = errors.New("content: media response has a content encoding")
	// ErrMediaResponse indicates that the response status cannot provide a complete object.
	ErrMediaResponse = errors.New("content: unusable media response")
	// ErrMediaTooLarge indicates that a transfer exceeds the client's byte limit.
	ErrMediaTooLarge = errors.New("content: media exceeds size limit")
	// ErrMediaSizeMismatch indicates that received bytes differ from the declared size.
	ErrMediaSizeMismatch = errors.New("content: media length differs from size_bytes")
	// ErrMediaIncomplete indicates that a transfer ended with a read error.
	ErrMediaIncomplete = errors.New("content: media transfer incomplete")
	// ErrDigestMismatch indicates that complete bytes do not match the signed digest.
	ErrDigestMismatch = errors.New("content: media sha256 mismatch")
	// ErrUnsupportedMedia indicates that the bytes are not in the supported inert allowlist.
	ErrUnsupportedMedia = errors.New("content: unsupported media type")
	// ErrKindMismatch indicates that the sniffed type conflicts with the declared kind.
	ErrKindMismatch = errors.New("content: media type does not match declared kind")
	// ErrMediaBudget indicates that image dimensions or frame count exceed local limits.
	ErrMediaBudget = errors.New("content: media exceeds decode budget")
)

// MediaBudget holds client-local limits; none are protocol fields.
type MediaBudget struct {
	MaxBytes            int64
	MaxWidth, MaxHeight int
	MaxPixels           int64
	MaxFrames           int
}

// VerifiedMedia contains bytes whose whole-object digest and inert type were verified.
type VerifiedMedia struct {
	Bytes         []byte    // Complete object bytes that passed digest verification.
	Type          MediaType // Type determined from the verified bytes.
	Width, Height int       // Image dimensions, or zero for video.
	Frames        int       // Image frame count, or zero for video.
}

// DefaultMediaBudget returns the reference client's 32 MiB, 16384-pixel, 40-million-pixel, and 1000-frame limits; these are client choices, not protocol fields.
func DefaultMediaBudget() MediaBudget {
	return MediaBudget{MaxBytes: 32 << 20, MaxWidth: 16384, MaxHeight: 16384, MaxPixels: 40_000_000, MaxFrames: 1000}
}

func mediaLimit(slot wire.MediaReference, b MediaBudget) (int64, error) {
	if slot.SizeBytes < 0 || b.MaxBytes <= 0 {
		return 0, ErrMediaTooLarge
	}
	if slot.SizeBytes > 0 {
		if slot.SizeBytes > b.MaxBytes {
			return 0, ErrMediaTooLarge
		}
		return slot.SizeBytes, nil
	}
	return b.MaxBytes, nil
}

// PrecheckMediaSize rejects a declared size that exceeds the client's transfer limit before fetching.
func PrecheckMediaSize(slot wire.MediaReference, b MediaBudget) error {
	if slot.SizeBytes < 0 || b.MaxBytes <= 0 || slot.SizeBytes > b.MaxBytes {
		return ErrMediaTooLarge
	}
	return nil
}

// CheckMediaResponse validates transfer headers before the response body is read.
func CheckMediaResponse(resp *http.Response, slot wire.MediaReference, b MediaBudget) error {
	if resp == nil || resp.StatusCode != http.StatusOK {
		return ErrMediaResponse
	}
	if resp.Uncompressed {
		return ErrEncodedResponse
	}
	if encodings := resp.Header.Values("Content-Encoding"); len(encodings) > 1 || (len(encodings) == 1 && !strings.EqualFold(strings.TrimSpace(encodings[0]), "identity")) {
		return ErrEncodedResponse
	}
	limit, err := mediaLimit(slot, b)
	if err != nil {
		return err
	}
	if resp.ContentLength >= 0 {
		if resp.ContentLength > limit {
			return ErrMediaTooLarge
		}
		if slot.SizeBytes > 0 && resp.ContentLength != slot.SizeBytes {
			return ErrMediaSizeMismatch
		}
	}
	return nil
}

// ReadVerifiedMedia reads a bounded complete object and verifies its declared size and SHA-256.
func ReadVerifiedMedia(r io.Reader, slot wire.MediaReference, b MediaBudget) ([]byte, error) {
	limit, err := mediaLimit(slot, b)
	if err != nil || r == nil {
		return nil, ErrMediaTooLarge
	}
	data, readErr := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(data)) > limit {
		return nil, ErrMediaTooLarge
	}
	if readErr != nil {
		return nil, ErrMediaIncomplete
	}
	if slot.SizeBytes > 0 && int64(len(data)) != slot.SizeBytes {
		return nil, ErrMediaSizeMismatch
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != slot.SHA256 {
		return nil, ErrDigestMismatch
	}
	return data, nil
}
