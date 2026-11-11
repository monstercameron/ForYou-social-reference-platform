package discovery

import (
	"bytes"
	"time"
)

// MaxPageBytes bounds one DiscoveryPage or ChangesPage.
const MaxPageBytes = 2 << 20

// EncodeDiscoveryPage writes a DiscoveryPage with exact nested message bytes.
func EncodeDiscoveryPage(generatedAt, expiresAt time.Time, messages [][]byte, nextCursor string) []byte {
	var output bytes.Buffer
	output.WriteString(`{"expires_at":"`)
	output.WriteString(expiresAt.UTC().Format("2006-01-02T15:04:05Z"))
	output.WriteString(`","generated_at":"`)
	output.WriteString(generatedAt.UTC().Format("2006-01-02T15:04:05Z"))
	output.WriteString(`","messages":[`)
	for i, message := range messages {
		if i > 0 {
			output.WriteByte(',')
		}
		output.Write(message)
	}
	output.WriteString(`],"next_cursor":`)
	if nextCursor == "" {
		output.WriteString("null")
	} else {
		output.WriteByte('"')
		output.WriteString(nextCursor)
		output.WriteByte('"')
	}
	output.WriteByte('}')
	return output.Bytes()
}

func discoveryPageFixedSize(generatedAt, expiresAt time.Time) int {
	return len(EncodeDiscoveryPage(generatedAt, expiresAt, nil, cursorPlaceholder))
}
