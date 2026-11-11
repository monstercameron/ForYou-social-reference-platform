package keys

import (
	"encoding/base64"
	"fmt"
)

var base64URL = base64.RawURLEncoding

// EncodeB64URL encodes bytes as unpadded RFC 4648 base64url.
func EncodeB64URL(b []byte) string { return base64URL.EncodeToString(b) }

// DecodeB64URL decodes exactly n bytes from canonical unpadded base64url.
func DecodeB64URL(s string, n int) ([]byte, error) {
	decoded, err := base64URL.DecodeString(s)
	if err != nil || len(decoded) != n || base64URL.EncodeToString(decoded) != s {
		return nil, fmt.Errorf("%w: invalid base64url", ErrBadEncoding)
	}
	return decoded, nil
}
