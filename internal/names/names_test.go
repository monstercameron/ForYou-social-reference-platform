package names

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
)

const authorDID = "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"

func seededKey(t *testing.T, first byte) keys.PrivateKey {
	t.Helper()
	seed := []byte{
		first, first + 1, first + 2, first + 3, first + 4, first + 5, first + 6, first + 7,
		first + 8, first + 9, first + 10, first + 11, first + 12, first + 13, first + 14, first + 15,
		first + 16, first + 17, first + 18, first + 19, first + 20, first + 21, first + 22, first + 23,
		first + 24, first + 25, first + 26, first + 27, first + 28, first + 29, first + 30, first + 31,
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func authorKey(t *testing.T) keys.PrivateKey { return seededKey(t, 0) }

func providerKey(t *testing.T) keys.PrivateKey { return seededKey(t, 32) }

func defaultClock() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }

func exampleRecords(t *testing.T) (NameRecord, NameRecord, []byte) {
	t.Helper()
	// Source: ForYou-Social-Ecosystem-Specification/examples/name-record.example.json.
	data, err := os.ReadFile("testdata/name-record.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Record     NameRecord `json:"record"`
		Withdrawal NameRecord `json:"withdrawal"`
	}
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	return example.Record, example.Withdrawal, data
}

func requireRecordEqual(t *testing.T, got, want NameRecord) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %#v; want %#v", got, want)
	}
}

func requireVerifyError(t *testing.T, r NameRecord, want error) {
	t.Helper()
	if err := Verify(r, defaultClock()); !errors.Is(err, want) {
		t.Fatalf("Verify() error = %v; want %v", err, want)
	}
}

func signRecord(t *testing.T, key keys.PrivateKey, label string, r NameRecord) NameRecord {
	t.Helper()
	canonical, err := canon.EncodeWithout(r, "author_signature")
	if err != nil {
		t.Fatal(err)
	}
	r.AuthorSignature, err = keys.Sign(key, label, canonical)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExampleRecords(t *testing.T) {
	record, withdrawal, fixture := exampleRecords(t)
	t.Run("build matches example record", func(t *testing.T) {
		name := "Zoë Example"
		avatar := &Avatar{URL: "https://media.example/avatars/zoe.png", SHA256: "08b860e2b03a68a952acf3d1c63b988f251a509aca0420ce984de33f31c8bf28", SizeBytes: 35}
		got, err := Build(authorKey(t), &name, avatar, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), nil)
		if err != nil {
			t.Fatal(err)
		}
		requireRecordEqual(t, got, record)
		if err := Verify(record, defaultClock()); err != nil {
			t.Fatalf("Verify(example) = %v", err)
		}
	})
	t.Run("build matches example withdrawal", func(t *testing.T) {
		got, err := Build(authorKey(t), nil, nil, defaultClock(), &record.NameID)
		if err != nil {
			t.Fatal(err)
		}
		requireRecordEqual(t, got, withdrawal)
		if err := Verify(withdrawal, defaultClock()); err != nil {
			t.Fatalf("Verify(withdrawal) = %v", err)
		}
	})
	t.Run("record encoding matches example JCS", func(t *testing.T) {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(fixture, &members); err != nil {
			t.Fatal(err)
		}
		want, err := canon.Canonicalize(members["record"], canon.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := Encode(record)
		if err != nil || string(got) != string(want) {
			t.Fatalf("Encode(record) = %s, %v; want %s", got, err, want)
		}
	})
	t.Run("withdrawal encoding matches example JCS", func(t *testing.T) {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(fixture, &members); err != nil {
			t.Fatal(err)
		}
		want, err := canon.Canonicalize(members["withdrawal"], canon.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := Encode(withdrawal)
		if err != nil || string(got) != string(want) {
			t.Fatalf("Encode(withdrawal) = %s, %v; want %s", got, err, want)
		}
	})
	var members map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &members); err != nil {
		t.Fatal(err)
	}
	t.Run("record parses, verifies, and encodes canonically", func(t *testing.T) {
		canonical, err := canon.Canonicalize(members["record"], canon.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(canonical)
		if err != nil {
			t.Fatal(err)
		}
		requireRecordEqual(t, parsed, record)
		if parsed.NameID != "fynr2:9d81b977f01a420f3cace01c30cdab9a87402e8b94c1283639f2a1bd2f4eeee7" || parsed.Avatar == nil || parsed.Avatar.URL != "https://media.example/avatars/zoe.png" || parsed.Avatar.SHA256 != "08b860e2b03a68a952acf3d1c63b988f251a509aca0420ce984de33f31c8bf28" || parsed.Avatar.SizeBytes != 35 {
			t.Fatalf("parsed example record = %#v", parsed)
		}
		if err := Verify(parsed, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("Verify(example) = %v", err)
		}
		if encoded, err := Encode(parsed); err != nil || string(encoded) != string(canonical) {
			t.Fatalf("Encode(Parse(record)) = %s, %v; want %s", encoded, err, canonical)
		}
	})
	t.Run("withdrawal parses and encodes canonically", func(t *testing.T) {
		canonical, err := canon.Canonicalize(members["withdrawal"], canon.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(canonical)
		if err != nil {
			t.Fatal(err)
		}
		requireRecordEqual(t, parsed, withdrawal)
		if parsed.Avatar != nil {
			t.Fatalf("Parse(withdrawal) = %#v, %v", parsed, err)
		}
		if err := Verify(parsed, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("Verify(withdrawal) = %v", err)
		}
		if roundTrip, err := Encode(parsed); err != nil || string(roundTrip) != string(canonical) {
			t.Fatalf("Encode(Parse(withdrawal)) = %s, %v; want %s", roundTrip, err, canonical)
		}
	})
}

func TestAvatarRules(t *testing.T) {
	name := "Zoë Example"
	validAvatar := &Avatar{URL: "https://media.example/avatars/zoe.png", SHA256: "08b860e2b03a68a952acf3d1c63b988f251a509aca0420ce984de33f31c8bf28", SizeBytes: 35}
	invalid := func(t *testing.T, avatar *Avatar, displayName *string) {
		t.Helper()
		if err := ValidateAvatar(avatar, displayName); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("ValidateAvatar() error = %v; want ErrInvalidRecord", err)
		}
	}
	t.Run("nil avatar is valid", func(t *testing.T) {
		if err := ValidateAvatar(nil, nil); err != nil {
			t.Fatalf("ValidateAvatar(nil) = %v", err)
		}
	})
	t.Run("withdrawn name cannot have avatar", func(t *testing.T) { invalid(t, validAvatar, nil) })
	t.Run("Build rejects an avatar on a withdrawn name", func(t *testing.T) {
		if _, err := Build(authorKey(t), nil, validAvatar, defaultClock(), nil); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Build() error = %v; want ErrInvalidRecord", err)
		}
	})
	t.Run("HTTP URL", func(t *testing.T) {
		a := *validAvatar
		a.URL = "http://media.example/avatar.png"
		invalid(t, &a, &name)
	})
	t.Run("userinfo URL", func(t *testing.T) {
		a := *validAvatar
		a.URL = "https://user@media.example/avatar.png"
		invalid(t, &a, &name)
	})
	t.Run("uppercase host URL", func(t *testing.T) {
		a := *validAvatar
		a.URL = "https://Media.example/avatar.png"
		invalid(t, &a, &name)
	})
	t.Run("URL exceeds avatar limit", func(t *testing.T) {
		a := *validAvatar
		a.URL = "https://media.example/" + strings.Repeat("a", 2027)
		invalid(t, &a, &name)
	})
	t.Run("uppercase digest", func(t *testing.T) { a := *validAvatar; a.SHA256 = strings.Repeat("A", 64); invalid(t, &a, &name) })
	t.Run("short digest", func(t *testing.T) { a := *validAvatar; a.SHA256 = strings.Repeat("a", 63); invalid(t, &a, &name) })
	t.Run("zero size", func(t *testing.T) { a := *validAvatar; a.SizeBytes = 0; invalid(t, &a, &name) })
	t.Run("size exceeds limit", func(t *testing.T) { a := *validAvatar; a.SizeBytes = 1048577; invalid(t, &a, &name) })
	t.Run("maximum size is accepted", func(t *testing.T) {
		a := *validAvatar
		a.SizeBytes = 1048576
		if err := ValidateAvatar(&a, &name); err != nil {
			t.Fatalf("ValidateAvatar(max size) = %v", err)
		}
	})
}

func TestAvatarParsing(t *testing.T) {
	t.Run("unknown avatar member is rejected", func(t *testing.T) {
		requireParseError(t, `{"name_id":"fynr2:9d81b977f01a420f3cace01c30cdab9a87402e8b94c1283639f2a1bd2f4eeee7","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","display_name":"Zoë Example","avatar":{"url":"https://media.example/avatars/zoe.png","sha256":"08b860e2b03a68a952acf3d1c63b988f251a509aca0420ce984de33f31c8bf28","size_bytes":35,"extra":true},"issued_at":"2026-11-11T00:00:00Z","previous_name_id":null,"author_signature":"k6HCo-NZ4zaqGNF7VWL0rTiDMfzM2G7LKy4xgUw7U0LtYmT0itI32jaCfM8aR7FvR7d4m_1FZLoyahFsXwU1Bw"}`)
	})
	t.Run("missing avatar member is rejected", func(t *testing.T) {
		requireParseError(t, `{"name_id":"fynr2:9d81b977f01a420f3cace01c30cdab9a87402e8b94c1283639f2a1bd2f4eeee7","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","display_name":"Zoë Example","issued_at":"2026-11-11T00:00:00Z","previous_name_id":null,"author_signature":"k6HCo-NZ4zaqGNF7VWL0rTiDMfzM2G7LKy4xgUw7U0LtYmT0itI32jaCfM8aR7FvR7d4m_1FZLoyahFsXwU1Bw"}`)
	})
	t.Run("string avatar is rejected", func(t *testing.T) {
		requireParseError(t, `{"name_id":"fynr2:9d81b977f01a420f3cace01c30cdab9a87402e8b94c1283639f2a1bd2f4eeee7","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","display_name":"Zoë Example","avatar":"https://media.example/avatars/zoe.png","issued_at":"2026-11-11T00:00:00Z","previous_name_id":null,"author_signature":"k6HCo-NZ4zaqGNF7VWL0rTiDMfzM2G7LKy4xgUw7U0LtYmT0itI32jaCfM8aR7FvR7d4m_1FZLoyahFsXwU1Bw"}`)
	})
	t.Run("fractional size is rejected", func(t *testing.T) {
		requireParseError(t, `{"name_id":"fynr2:9d81b977f01a420f3cace01c30cdab9a87402e8b94c1283639f2a1bd2f4eeee7","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","display_name":"Zoë Example","avatar":{"url":"https://media.example/avatars/zoe.png","sha256":"08b860e2b03a68a952acf3d1c63b988f251a509aca0420ce984de33f31c8bf28","size_bytes":35.5},"issued_at":"2026-11-11T00:00:00Z","previous_name_id":null,"author_signature":"k6HCo-NZ4zaqGNF7VWL0rTiDMfzM2G7LKy4xgUw7U0LtYmT0itI32jaCfM8aR7FvR7d4m_1FZLoyahFsXwU1Bw"}`)
	})
}

func requireParseError(t *testing.T, data string) {
	t.Helper()
	if _, err := Parse([]byte(data)); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Parse() error = %v; want ErrInvalidRecord", err)
	}
}

func TestAvatarTampering(t *testing.T) {
	record, _, _ := exampleRecords(t)
	t.Run("changed URL", func(t *testing.T) {
		changed := record
		avatar := *record.Avatar
		avatar.URL = "https://media.example/avatars/other.png"
		changed.Avatar = &avatar
		requireVerifyError(t, changed, ErrIDMismatch)
	})
	t.Run("changed digest", func(t *testing.T) {
		changed := record
		avatar := *record.Avatar
		avatar.SHA256 = "18b860e2b03a68a952acf3d1c63b988f251a509aca0420ce984de33f31c8bf28"
		changed.Avatar = &avatar
		requireVerifyError(t, changed, ErrIDMismatch)
	})
	t.Run("changed size", func(t *testing.T) {
		changed := record
		avatar := *record.Avatar
		avatar.SizeBytes = 36
		changed.Avatar = &avatar
		requireVerifyError(t, changed, ErrIDMismatch)
	})
	t.Run("null avatar", func(t *testing.T) {
		changed := record
		changed.Avatar = nil
		requireVerifyError(t, changed, ErrIDMismatch)
	})
}

func TestVerificationOrder(t *testing.T) {
	record, withdrawal, fixture := exampleRecords(t)
	t.Run("avatar on withdrawn name is rejected before name ID", func(t *testing.T) {
		changed := withdrawal
		avatar := *record.Avatar
		changed.Avatar = &avatar
		requireVerifyError(t, changed, ErrInvalidRecord)
	})
	t.Run("invalid avatar URL is rejected before name ID", func(t *testing.T) {
		changed := record
		avatar := *record.Avatar
		avatar.URL = "https://Media.example/avatars/zoe.png"
		changed.Avatar = &avatar
		requireVerifyError(t, changed, ErrInvalidRecord)
	})
	t.Run("changed display name reaches ID mismatch", func(t *testing.T) {
		changed := record
		name := "Zoe Example"
		changed.DisplayName = &name
		requireVerifyError(t, changed, ErrIDMismatch)
	})
	t.Run("invalid display name reaches name validation", func(t *testing.T) {
		changed := record
		name := "Ada\u202e"
		changed.DisplayName = &name
		requireVerifyError(t, changed, ErrInvalidName)
	})
	t.Run("changed issue time reaches ID mismatch", func(t *testing.T) {
		changed := record
		changed.IssuedAt = "2026-11-11T00:00:01Z"
		requireVerifyError(t, changed, ErrIDMismatch)
	})
	t.Run("provider author reaches ID mismatch", func(t *testing.T) {
		changed := record
		changed.AuthorID = keys.DIDOf(providerKey(t))
		requireVerifyError(t, changed, ErrIDMismatch)
	})
	t.Run("changed previous ID reaches ID mismatch", func(t *testing.T) {
		changed := record
		previous := IDPrefix + strings.Repeat("a", 64)
		changed.PreviousNameID = &previous
		requireVerifyError(t, changed, ErrIDMismatch)
	})
	t.Run("author resigning changed name still reaches ID mismatch", func(t *testing.T) {
		changed := record
		name := "Different"
		changed.DisplayName = &name
		changed = signRecord(t, authorKey(t), "name-record", changed)
		requireVerifyError(t, changed, ErrIDMismatch)
	})
	t.Run("wrong signature label is rejected", func(t *testing.T) {
		requireVerifyError(t, signRecord(t, authorKey(t), "message", record), ErrBadSignature)
	})
	t.Run("provider signature is rejected", func(t *testing.T) {
		requireVerifyError(t, signRecord(t, providerKey(t), "name-record", record), ErrBadSignature)
	})
	t.Run("extra member is structurally rejected", func(t *testing.T) {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(fixture, &object); err != nil {
			t.Fatal(err)
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(object["record"], &members); err != nil {
			t.Fatal(err)
		}
		members["extra"] = json.RawMessage(`1`)
		data, err := json.Marshal(members)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Parse(extra) error = %v; want ErrInvalidRecord", err)
		}
	})
	t.Run("missing previous ID is structurally rejected", func(t *testing.T) {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(fixture, &object); err != nil {
			t.Fatal(err)
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(object["record"], &members); err != nil {
			t.Fatal(err)
		}
		delete(members, "previous_name_id")
		data, err := json.Marshal(members)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Parse(missing previous_name_id) error = %v; want ErrInvalidRecord", err)
		}
	})
	t.Run("fynr1 prefix is structurally rejected", func(t *testing.T) {
		changed := record
		changed.NameID = "fynr1:" + strings.Repeat("a", 64)
		requireVerifyError(t, changed, ErrInvalidRecord)
	})
}

func TestClockTolerance(t *testing.T) {
	t.Run("exactly sixty seconds ahead passes", func(t *testing.T) {
		r, err := Build(authorKey(t), stringPointer("Ada"), nil, defaultClock().Add(60*time.Second), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := Verify(r, defaultClock()); err != nil {
			t.Fatalf("Verify() = %v", err)
		}
	})
	t.Run("sixty one seconds ahead fails", func(t *testing.T) {
		r, err := Build(authorKey(t), stringPointer("Ada"), nil, defaultClock().Add(61*time.Second), nil)
		if err != nil {
			t.Fatal(err)
		}
		requireVerifyError(t, r, ErrFuture)
	})
}

func stringPointer(s string) *string { return &s }

func TestNameRules(t *testing.T) {
	valid := func(t *testing.T, value string) {
		t.Helper()
		if err := ValidateName(value); err != nil {
			t.Fatalf("ValidateName() = %v", err)
		}
	}
	invalid := func(t *testing.T, value string) {
		t.Helper()
		if err := ValidateName(value); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("ValidateName() = %v; want ErrInvalidName", err)
		}
	}
	t.Run("Ada", func(t *testing.T) { valid(t, "Ada") })
	t.Run("NFC Zoë", func(t *testing.T) { valid(t, "Zoë") })
	t.Run("Ana María", func(t *testing.T) { valid(t, "Ana María") })
	t.Run("CJK name", func(t *testing.T) { valid(t, "李小龙") })
	t.Run("Arabic name", func(t *testing.T) { valid(t, "مريم") })
	t.Run("64 ASCII scalars", func(t *testing.T) { valid(t, strings.Repeat("a", 64)) })
	t.Run("64 four byte scalars", func(t *testing.T) { valid(t, strings.Repeat("😀", 64)) })
	t.Run("punctuation", func(t *testing.T) { valid(t, "Ada Lovelace (test)") })
	t.Run("Unicode 15 CJK Extension H", func(t *testing.T) { valid(t, "𱍐") })
	t.Run("Unicode 15 Kawi", func(t *testing.T) { valid(t, "𑼀") })
	t.Run("control character", func(t *testing.T) { invalid(t, "Ada\u0007") })
	t.Run("line feed", func(t *testing.T) { invalid(t, "Ada\nB") })
	t.Run("bidi override", func(t *testing.T) { invalid(t, "Ada\u202eB") })
	t.Run("bidi isolate", func(t *testing.T) { invalid(t, "\u2067Ada") })
	t.Run("zero width joiner", func(t *testing.T) { invalid(t, "A\u200dB") })
	t.Run("line separator", func(t *testing.T) { invalid(t, "Ada\u2028B") })
	t.Run("private use", func(t *testing.T) { invalid(t, "Ada\ue000") })
	t.Run("Unicode 15 unassigned", func(t *testing.T) { invalid(t, "Ada\u0378") })
	t.Run("assigned only after Unicode 15 U+1C89", func(t *testing.T) { invalid(t, "Ada\u1c89") })
	t.Run("assigned only after Unicode 15 U+2FFC", func(t *testing.T) { invalid(t, "Ada\u2ffc") })
	t.Run("too many ASCII scalars", func(t *testing.T) { invalid(t, strings.Repeat("a", 65)) })
	t.Run("too many four byte scalars", func(t *testing.T) { invalid(t, strings.Repeat("😀", 65)) })
	t.Run("empty", func(t *testing.T) { invalid(t, "") })
	t.Run("leading ASCII space", func(t *testing.T) { invalid(t, " Ada") })
	t.Run("trailing ASCII space", func(t *testing.T) { invalid(t, "Ada ") })
	t.Run("leading ideographic space", func(t *testing.T) { invalid(t, "\u3000Ada") })
	t.Run("two consecutive spaces", func(t *testing.T) { invalid(t, "Ada  B") })
	t.Run("decomposed name", func(t *testing.T) { invalid(t, "Zoe\u0308") })
	t.Run("invalid UTF-8", func(t *testing.T) { invalid(t, "\xff") })
	t.Run("zero width no-break space", func(t *testing.T) { invalid(t, "A\ufeffB") })
	t.Run("trailing no-break space", func(t *testing.T) { invalid(t, "Ada\u00a0") })
	t.Run("NormalizeName composes decomposed input", func(t *testing.T) {
		got, err := NormalizeName("Zoe\u0308")
		if err != nil || got != "Zoë" {
			t.Fatalf("NormalizeName() = %q, %v; want Zoë", got, err)
		}
	})
}

func TestNameFolding(t *testing.T) {
	t.Run("case insensitive composed name", func(t *testing.T) { requireSameName(t, "Zoë Example", "ZOË EXAMPLE", true) })
	t.Run("ASCII k and Kelvin sign", func(t *testing.T) { requireSameName(t, "k", "K", true) })
	t.Run("ASCII K and Kelvin sign", func(t *testing.T) { requireSameName(t, "K", "K", true) })
	t.Run("case insensitive Zoë", func(t *testing.T) { requireSameName(t, "Zoë", "zoë", true) })
	t.Run("NFC query compares equal", func(t *testing.T) { requireSameName(t, "Zoë", "zoë", true) })
	t.Run("sharp s does not full fold to SS", func(t *testing.T) { requireSameName(t, "straße", "STRASSE", false) })
	t.Run("sharp s capital simple fold", func(t *testing.T) { requireSameName(t, "ß", "ẞ", true) })
	t.Run("trailing space differs", func(t *testing.T) { requireSameName(t, "Ada", "Ada ", false) })
	t.Run("later assigned orbit member does not match", func(t *testing.T) { requireSameName(t, "ɤ", "Ɤ", false) })
}

func requireSameName(t *testing.T, a, b string, want bool) {
	t.Helper()
	if got := SameName(a, b); got != want {
		t.Fatalf("SameName(%q, %q) = %t; want %t", a, b, got, want)
	}
}

func TestCheckQuery(t *testing.T) {
	t.Run("rejects empty query", func(t *testing.T) { requireQueryError(t, "") })
	t.Run("rejects excessive scalar count", func(t *testing.T) { requireQueryError(t, strings.Repeat("a", 65)) })
	t.Run("rejects invalid UTF-8", func(t *testing.T) { requireQueryError(t, "\xff") })
	t.Run("returns NFC query", func(t *testing.T) {
		got, err := CheckQuery("ZOË")
		if err != nil || got != "ZOË" {
			t.Fatalf("CheckQuery() = %q, %v; want ZOË", got, err)
		}
	})
}

func requireQueryError(t *testing.T, query string) {
	t.Helper()
	if _, err := CheckQuery(query); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("CheckQuery() error = %v; want ErrInvalidQuery", err)
	}
}

func TestCurrentRecord(t *testing.T) {
	root := authorKey(t)
	first, err := Build(root, stringPointer("First"), nil, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(root, stringPointer("Second"), nil, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), &first.NameID)
	if err != nil {
		t.Fatal(err)
	}
	backdated, err := Build(root, stringPointer("Backdated"), nil, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), &second.NameID)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("newest wins in forward input order", func(t *testing.T) { requireCurrent(t, authorDID, []NameRecord{first, second, backdated}, second, true) })
	t.Run("newest wins in reverse input order", func(t *testing.T) { requireCurrent(t, authorDID, []NameRecord{backdated, second, first}, second, true) })
	t.Run("tie chooses hard-coded smallest ID", func(t *testing.T) {
		alpha, err := Build(root, stringPointer("Alpha"), nil, firstTime(), nil)
		if err != nil {
			t.Fatal(err)
		}
		beta, err := Build(root, stringPointer("Beta"), nil, firstTime(), nil)
		if err != nil {
			t.Fatal(err)
		}
		gamma, err := Build(root, stringPointer("Gamma"), nil, firstTime(), nil)
		if err != nil {
			t.Fatal(err)
		}
		const expectedID = "fynr2:52c69deb4422c248a6f357920ca60ced11de7d90556e7330ebdf80ad5e687901"
		if gamma.NameID != expectedID {
			t.Fatalf("Gamma name_id = %s; want %s", gamma.NameID, expectedID)
		}
		current, ok := Current(authorDID, []NameRecord{alpha, beta, gamma}, defaultClock())
		if !ok || current.NameID != expectedID {
			t.Fatalf("Current() = %s, %t; want hard-coded ID %s", current.NameID, ok, expectedID)
		}
	})
	t.Run("forged later record cannot displace valid record", func(t *testing.T) {
		forged, err := Build(root, stringPointer("Forged"), nil, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), nil)
		if err != nil {
			t.Fatal(err)
		}
		forged.DisplayName = stringPointer("Changed")
		forged = signRecord(t, root, "name-record", forged)
		requireCurrent(t, authorDID, []NameRecord{first, forged}, first, true)
	})
	t.Run("only forged record gives no result", func(t *testing.T) {
		forged := first
		forged.DisplayName = stringPointer("Changed")
		forged = signRecord(t, root, "name-record", forged)
		if _, ok := Current(authorDID, []NameRecord{forged}, defaultClock()); ok {
			t.Fatal("Current() selected an invalid record")
		}
	})
	t.Run("another author is ignored", func(t *testing.T) {
		other, err := Build(providerKey(t), stringPointer("Other"), nil, firstTime(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := Current(authorDID, []NameRecord{other}, defaultClock()); ok {
			t.Fatal("Current() selected another author's record")
		}
	})
	t.Run("example withdrawal is current", func(t *testing.T) {
		record, withdrawal, _ := exampleRecords(t)
		requireCurrent(t, authorDID, []NameRecord{record, withdrawal}, withdrawal, true)
	})
}

func firstTime() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }

func requireCurrent(t *testing.T, author string, records []NameRecord, want NameRecord, okWant bool) {
	t.Helper()
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	got, ok := Current(author, records, now)
	if ok != okWant {
		t.Fatalf("Current() ok = %t; want %t", ok, okWant)
	}
	if ok {
		requireRecordEqual(t, got, want)
	}
}

func TestNamesPage(t *testing.T) {
	record, _, _ := exampleRecords(t)
	t.Run("empty page encodes empty array", func(t *testing.T) {
		got, err := EncodePage(NamesPage{})
		if err != nil || string(got) != `{"names":[],"next_cursor":null}` {
			t.Fatalf("EncodePage(empty) = %s, %v", got, err)
		}
	})
	t.Run("single record page has literal JCS encoding", func(t *testing.T) {
		got, err := EncodePage(NamesPage{Names: []NameRecord{record}})
		const want = `{"names":[{"author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","author_signature":"k6HCo-NZ4zaqGNF7VWL0rTiDMfzM2G7LKy4xgUw7U0LtYmT0itI32jaCfM8aR7FvR7d4m_1FZLoyahFsXwU1Bw","avatar":{"sha256":"08b860e2b03a68a952acf3d1c63b988f251a509aca0420ce984de33f31c8bf28","size_bytes":35,"url":"https://media.example/avatars/zoe.png"},"display_name":"Zoë Example","issued_at":"2026-11-11T00:00:00Z","name_id":"fynr2:9d81b977f01a420f3cace01c30cdab9a87402e8b94c1283639f2a1bd2f4eeee7","previous_name_id":null}],"next_cursor":null}`
		if err != nil || string(got) != want {
			t.Fatalf("EncodePage() = %s, %v; want %s", got, err, want)
		}
	})
	t.Run("round trips a valid cursor", func(t *testing.T) {
		page, err := ParsePage([]byte(`{"names":[],"next_cursor":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`))
		if err != nil || page.NextCursor == nil || *page.NextCursor != strings.Repeat("A", 43) {
			t.Fatalf("ParsePage() = %#v, %v", page, err)
		}
	})
	t.Run("rejects 51 records", func(t *testing.T) {
		recordJSON, err := Encode(record)
		if err != nil {
			t.Fatal(err)
		}
		tooMany := `{"names":[` + strings.TrimSuffix(strings.Repeat(string(recordJSON)+",", 51), ",") + `],"next_cursor":null}`
		if _, err := ParsePage([]byte(tooMany)); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("ParsePage() error = %v", err)
		}
	})
	t.Run("rejects extra generated_at member", func(t *testing.T) {
		requirePageError(t, `{"names":[],"next_cursor":null,"generated_at":"2026-11-11T00:00:00Z"}`)
	})
	t.Run("rejects missing next_cursor", func(t *testing.T) { requirePageError(t, `{"names":[]}`) })
	t.Run("rejects 42 character cursor", func(t *testing.T) {
		requirePageError(t, `{"names":[],"next_cursor":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`)
	})
	t.Run("rejects noncanonical 43 character cursor", func(t *testing.T) {
		requirePageError(t, `{"names":[],"next_cursor":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB"}`)
	})
	t.Run("Encode rejects structurally invalid record", func(t *testing.T) {
		invalid := record
		invalid.NameID = "fynr1:" + strings.Repeat("a", 64)
		if _, err := Encode(invalid); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Encode() error = %v", err)
		}
	})
	t.Run("EncodePage rejects structurally invalid page", func(t *testing.T) {
		if _, err := EncodePage(NamesPage{Names: make([]NameRecord, 51)}); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("EncodePage() error = %v", err)
		}
	})
}

func requirePageError(t *testing.T, data string) {
	t.Helper()
	if _, err := ParsePage([]byte(data)); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("ParsePage() error = %v; want ErrInvalidRecord", err)
	}
}

func TestFingerprint(t *testing.T) {
	const want = "ef9b 5305 5830 b478"
	if got := Fingerprint(authorDID); got != want {
		t.Fatalf("Fingerprint() = %q; want %q", got, want)
	}
}

func TestUnicodeTables(t *testing.T) {
	t.Run("U+0378 is excluded", func(t *testing.T) { requireExcluded(t, '\u0378', true) })
	t.Run("U+1C89 is excluded", func(t *testing.T) { requireExcluded(t, '\u1c89', true) })
	t.Run("U+2FFC is excluded", func(t *testing.T) { requireExcluded(t, '\u2ffc', true) })
	t.Run("private use is excluded", func(t *testing.T) { requireExcluded(t, '\ue000', true) })
	t.Run("format character is excluded", func(t *testing.T) { requireExcluded(t, '\u200d', true) })
	t.Run("line separator is excluded", func(t *testing.T) { requireExcluded(t, '\u2028', true) })
	t.Run("noncharacter is excluded", func(t *testing.T) { requireExcluded(t, '\U0010ffff', true) })
	t.Run("Latin A is assigned", func(t *testing.T) { requireExcluded(t, 'A', false) })
	t.Run("Latin é is assigned", func(t *testing.T) { requireExcluded(t, 'é', false) })
	t.Run("CJK Extension H is assigned", func(t *testing.T) { requireExcluded(t, '\U00031350', false) })
	t.Run("Kawi is assigned", func(t *testing.T) { requireExcluded(t, '\U00011f00', false) })
	t.Run("White_Space NEL", func(t *testing.T) { requireWhiteSpace(t, '\u0085', true) })
	t.Run("White_Space Ogham space", func(t *testing.T) { requireWhiteSpace(t, '\u1680', true) })
	t.Run("White_Space ideographic space", func(t *testing.T) { requireWhiteSpace(t, '\u3000', true) })
	t.Run("zero width space is not White_Space", func(t *testing.T) { requireWhiteSpace(t, '\u200b', false) })
	t.Run("BOM is not White_Space", func(t *testing.T) { requireWhiteSpace(t, '\ufeff', false) })
}

func requireExcluded(t *testing.T, r rune, want bool) {
	t.Helper()
	if got := unicode.Is(ucd15Excluded, r); got != want {
		t.Fatalf("ucd15Excluded(%U) = %t; want %t", r, got, want)
	}
}

func requireWhiteSpace(t *testing.T, r rune, want bool) {
	t.Helper()
	if got := unicode.Is(unicode.White_Space, r); got != want {
		t.Fatalf("White_Space(%U) = %t; want %t", r, got, want)
	}
}
