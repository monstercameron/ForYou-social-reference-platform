package discovery

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
)

// The fixtures are exact copies of specification examples/post-version-chain.json, examples/post-with-refs.json and examples/post-comment-thread.json.
func TestIndexSelectsSpecificationExamples(t *testing.T) {
	index := NewIndex()
	addExample := func(t *testing.T, name, key string, position int, sequence uint64) Record {
		t.Helper()
		record := exampleRecord(t, name, key, position, sequence)
		if !index.Add(record) {
			t.Fatalf("Add(%s) returned false", record.MessageID)
		}
		return record
	}
	addExample(t, "post-version-chain.json", "messages", 0, 1)
	addExample(t, "post-version-chain.json", "messages", 1, 2)
	addExample(t, "post-version-chain.json", "messages", 2, 3)
	addExample(t, "post-version-chain.json", "messages", 3, 4)
	addExample(t, "post-version-chain.json", "messages", 4, 5)
	addExample(t, "post-with-refs.json", "message", 0, 6)
	addExample(t, "post-comment-thread.json", "messages", 0, 7)
	addExample(t, "post-comment-thread.json", "messages", 1, 8)
	addExample(t, "post-comment-thread.json", "messages", 2, 9)
	t.Run("global ordering", func(t *testing.T) {
		query, _, err := ParseListQuery(Messages, "", "")
		if err != nil {
			t.Fatal(err)
		}
		records := index.Select(query)
		assertRecordIDs(t, records,
			"fy1:e703e832f979cb8c1dc232ad719a4f09117617c7852774121e90b4ca2e345262",
			"fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf",
			"fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b",
			"fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72",
			"fy1:fd23c3b0539258425ec1c0faa056465777e5d5b2d92ef390a71f164748b1f6f8",
			"fy1:42b8efb077bb3542a6a49cdab628e002571d5efcd841fb187142faa80c4a3fad",
			"fy1:52ef5b8da8d2dfe1da62510f148807fe1e098ba633299e2722a7c5f529981f67",
			"fy1:2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18",
			"fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f")
	})
	t.Run("version successors", func(t *testing.T) {
		query, _, err := ParseListQuery(Versions, "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f", "")
		if err != nil {
			t.Fatal(err)
		}
		assertRecordIDs(t, index.Select(query),
			"fy1:569bfd9a8713b5d315c341b5cae977faf2abf9f623cf78aaab2198c76bc11ddf",
			"fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72")
	})
	t.Run("direct replies", func(t *testing.T) {
		query, _, err := ParseListQuery(Replies, "fy1:52ef5b8da8d2dfe1da62510f148807fe1e098ba633299e2722a7c5f529981f67", "")
		if err != nil {
			t.Fatal(err)
		}
		assertRecordIDs(t, index.Select(query), "fy1:42b8efb077bb3542a6a49cdab628e002571d5efcd841fb187142faa80c4a3fad")
	})
	t.Run("declared DID references", func(t *testing.T) {
		query, _, err := ParseListQuery(References, "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG", "")
		if err != nil {
			t.Fatal(err)
		}
		assertRecordIDs(t, index.Select(query), "fy1:2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18")
	})
	t.Run("declared message reference", func(t *testing.T) {
		query, _, err := ParseListQuery(References, "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f", "")
		if err != nil {
			t.Fatal(err)
		}
		assertRecordIDs(t, index.Select(query), "fy1:2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18")
	})
	t.Run("exact author and day", func(t *testing.T) {
		query, _, err := ParseListQuery(AuthorMessages, "", "author_id=did%3Akey%3Az6Mkg26jczDiqsPK4momfvhZTTyFefWEyxYiSisFJ2wWJFkg&date=2026-11-11")
		if err != nil {
			t.Fatal(err)
		}
		assertRecordIDs(t, index.Select(query),
			"fy1:e703e832f979cb8c1dc232ad719a4f09117617c7852774121e90b4ca2e345262",
			"fy1:42b8efb077bb3542a6a49cdab628e002571d5efcd841fb187142faa80c4a3fad")
	})
}

func TestIndexReferencesIgnoreUndeclaredText(t *testing.T) {
	index := NewIndex()
	if !index.Add(exampleRecord(t, "post-with-refs.json", "message", 0, 1)) {
		t.Fatal("Add(post with refs) returned false")
	}
	textOnly, err := ParseRecord([]byte(`{"message_id":"fy1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author_id":"did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG","timestamp":"2026-11-11T00:00:40Z","payload":{"text":"did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG"}}`), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !index.Add(textOnly) {
		t.Fatal("Add(text-only reference string) returned false")
	}
	query, _, err := ParseListQuery(References, "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG", "")
	if err != nil {
		t.Fatal(err)
	}
	assertRecordIDs(t, index.Select(query), "fy1:2504bdd7689aeb36aef03f22ca4ab591d0d5347bc0afec12635810d3dbbfba18")
}

func TestIndexDuplicateRemovalAndSequences(t *testing.T) {
	record := exampleRecord(t, "post-version-chain.json", "messages", 0, 4)
	index := NewIndex()
	if !index.Add(record) {
		t.Fatal("first Add returned false")
	}
	replacement := record
	replacement.Bytes = []byte("second representation")
	replacement.Sequence = 5
	if index.Add(replacement) {
		t.Fatal("duplicate Add returned true")
	}
	if got := index.MaxSequence(); got != 4 {
		t.Fatalf("MaxSequence() after duplicate = %d; want 4", got)
	}
	got, ok := index.Get(record.MessageID)
	if !ok || string(got.Bytes) == string(replacement.Bytes) || got.Sequence != 4 {
		t.Fatalf("Get() = (%+v, %t); duplicate changed first representation", got, ok)
	}
	if !index.Remove(record.MessageID) || index.Remove(record.MessageID) {
		t.Fatal("Remove did not remove exactly one indexed record")
	}
	if got := index.MaxSequence(); got != 4 {
		t.Fatalf("MaxSequence() after Remove = %d; want 4", got)
	}
	query, _, err := ParseListQuery(Messages, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Select(query)) != 0 || len(index.After(0, 10)) != 0 {
		t.Fatal("removed record remained visible")
	}
	index.ObserveSequence(12)
	if got := index.MaxSequence(); got != 12 {
		t.Fatalf("MaxSequence() = %d; want 12", got)
	}
}

func TestIndexAfterSequenceOrder(t *testing.T) {
	index := NewIndex()
	first := exampleRecord(t, "post-version-chain.json", "messages", 0, 2)
	second := exampleRecord(t, "post-version-chain.json", "messages", 1, 1)
	if !index.Add(first) || !index.Add(second) {
		t.Fatal("Add returned false")
	}
	if got := index.After(0, 1); len(got) != 1 || got[0].Sequence != 1 {
		t.Fatalf("After(0, 1) = %+v; want sequence 1", got)
	}
	if got := index.After(1, 5); len(got) != 1 || got[0].Sequence != 2 {
		t.Fatalf("After(1, 5) = %+v; want sequence 2", got)
	}
}

func TestIndexSelectWithinBudget(t *testing.T) {
	index := NewIndex()
	newer := Record{MessageID: "m3", AuthorID: "author", Timestamp: time.Date(2026, 11, 11, 0, 0, 3, 0, time.UTC), Sequence: 3, Bytes: []byte("0123456789")}
	middle := Record{MessageID: "m2", AuthorID: "author", Timestamp: time.Date(2026, 11, 11, 0, 0, 2, 0, time.UTC), Sequence: 2, Bytes: []byte("0123456789")}
	older := Record{MessageID: "m1", AuthorID: "author", Timestamp: time.Date(2026, 11, 11, 0, 0, 1, 0, time.UTC), Sequence: 1, Bytes: []byte("0123456789")}
	if !index.Add(older) || !index.Add(newer) || !index.Add(middle) {
		t.Fatal("Add returned false")
	}
	query, _, err := ParseListQuery(Messages, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("matches within the budget are returned in full", func(t *testing.T) {
		records, err := index.SelectWithin(query, 30)
		if err != nil {
			t.Fatalf("SelectWithin(30) error = %v", err)
		}
		assertRecordIDs(t, records, "m3", "m2", "m1")
	})
	t.Run("matches beyond the budget are busy rather than truncated", func(t *testing.T) {
		records, err := index.SelectWithin(query, 29)
		if !errors.Is(err, ErrBusy) || records != nil {
			t.Fatalf("SelectWithin(29) = %d records, error %v; want nil and ErrBusy", len(records), err)
		}
	})
}

func TestParseRecordUsesExactFieldNames(t *testing.T) {
	t.Run("case variant does not populate message id", func(t *testing.T) {
		message := []byte(`{"MESSAGE_ID":"fy1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author_id":"author","timestamp":"2026-11-11T00:00:00Z","payload":{}}`)
		if _, err := ParseRecord(message, 1); err == nil {
			t.Fatal("ParseRecord accepted a case-variant message_id")
		}
	})
	t.Run("case variant does not populate payload", func(t *testing.T) {
		message := []byte(`{"message_id":"fy1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author_id":"author","timestamp":"2026-11-11T00:00:00Z","Payload":{}}`)
		if _, err := ParseRecord(message, 1); err == nil {
			t.Fatal("ParseRecord accepted a case-variant Payload")
		}
	})
}

func exampleRecord(t *testing.T, filename, messagesKey string, position int, sequence uint64) Record {
	t.Helper()
	data, err := os.ReadFile("testdata/" + filename)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Message  json.RawMessage   `json:"message"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	switch messagesKey {
	case "message":
		raw = fixture.Message
	case "messages":
		if position < 0 || position >= len(fixture.Messages) {
			t.Fatalf("message position %d is outside %s", position, filename)
		}
		raw = fixture.Messages[position]
	default:
		t.Fatalf("unknown fixture messages key %q", messagesKey)
	}
	canonical, err := canon.Canonicalize(raw, canon.MessageLimits)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ParseRecord(canonical, sequence)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func assertRecordIDs(t *testing.T, records []Record, want ...string) {
	t.Helper()
	got := make([]string, len(records))
	for i, record := range records {
		got[i] = record.MessageID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record IDs = %q; want %q", got, want)
	}
}
