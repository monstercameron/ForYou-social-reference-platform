package discovery

import (
	"errors"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
)

// Record is the indexable view of one stored, verified message.
type Record struct {
	MessageID  string
	AuthorID   string
	Timestamp  time.Time
	ReplyTo    string
	Supersedes string
	RefTargets []string
	Sequence   uint64
	Bytes      []byte
}

// ParseRecord decodes retrieval fields from exact message bytes without verifying its signature.
func ParseRecord(b []byte, sequence uint64) (Record, error) {
	value, err := canon.Parse(b, canon.MessageLimits)
	if err != nil {
		return Record{}, err
	}
	message, ok := value.(map[string]any)
	if !ok {
		return Record{}, errors.New("message must be an object")
	}
	messageID, ok := message["message_id"].(string)
	if !ok || !isMessageID(messageID) {
		return Record{}, errors.New("invalid message_id")
	}
	authorID, err := stringField(message, "author_id", true)
	if err != nil {
		return Record{}, err
	}
	timestampText, err := stringField(message, "timestamp", true)
	if err != nil {
		return Record{}, err
	}
	timestamp, err := parseTimestamp(timestampText)
	if err != nil {
		return Record{}, errors.New("invalid timestamp")
	}
	payload, ok := message["payload"].(map[string]any)
	if !ok {
		return Record{}, errors.New("payload must be an object")
	}
	replyTo, err := stringField(payload, "reply_to", false)
	if err != nil {
		return Record{}, err
	}
	supersedes, err := stringField(payload, "supersedes", false)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		MessageID:  messageID,
		AuthorID:   authorID,
		Timestamp:  timestamp,
		ReplyTo:    replyTo,
		Supersedes: supersedes,
		Sequence:   sequence,
		Bytes:      append([]byte(nil), b...),
	}
	if refsValue, exists := payload["refs"]; exists {
		refs, ok := refsValue.([]any)
		if !ok {
			return Record{}, errors.New("refs must be an array")
		}
		for _, refValue := range refs {
			ref, ok := refValue.(map[string]any)
			if !ok {
				return Record{}, errors.New("ref must be an object")
			}
			target, err := stringField(ref, "target", true)
			if err != nil {
				return Record{}, err
			}
			record.RefTargets = append(record.RefTargets, target)
		}
	}
	return record, nil
}

func stringField(object map[string]any, name string, required bool) (string, error) {
	value, exists := object[name]
	if !exists && !required {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", errors.New("message field must be a string")
	}
	return text, nil
}

// Before orders records by descending timestamp and ascending message ID.
func Before(a, b Record) bool {
	if !a.Timestamp.Equal(b.Timestamp) {
		return a.Timestamp.After(b.Timestamp)
	}
	return a.MessageID < b.MessageID
}

func insertRecord(records []Record, record Record) []Record {
	index := sort.Search(len(records), func(i int) bool { return !Before(records[i], record) })
	records = append(records, Record{})
	copy(records[index+1:], records[index:])
	records[index] = record
	return records
}

func removeRecord(records []Record, record Record) []Record {
	index := sort.Search(len(records), func(i int) bool { return !Before(records[i], record) })
	if index < len(records) && records[index].MessageID == record.MessageID {
		return append(records[:index], records[index+1:]...)
	}
	return records
}

func cloneRecord(record Record) Record {
	record.Bytes = append([]byte(nil), record.Bytes...)
	record.RefTargets = append([]string(nil), record.RefTargets...)
	return record
}
