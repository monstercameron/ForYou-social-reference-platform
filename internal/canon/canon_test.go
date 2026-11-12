package canon

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// The fixture is copied byte-for-byte from the specification's tests/security-vectors.json.
func TestSecurityVector(t *testing.T) {
	data, err := os.ReadFile("testdata/security-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	vector, err := Parse(data, Limits{MaxBytes: len(data), MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	fixture := vector.(map[string]any)
	message := fixture["message"].(map[string]any)
	delete(message, "message_id")
	delete(message, "signature")
	delete(message, "submission_receipt")
	want, err := hex.DecodeString(fixture["intent_preimage_hex"].(string))
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "ForYou/1/intent\n"
	if !bytes.Equal(want[:16], []byte(prefix)) {
		t.Fatalf("intent preimage prefix = %q; want %q", want[:16], prefix)
	}
	want = want[16:]
	if len(want) != 232 {
		t.Fatalf("intent JCS length = %d; want 232", len(want))
	}
	got, err := Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal vector = %q; want %q", got, want)
	}
	encoded, err := Parse(data, Limits{MaxBytes: len(data), MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	without, err := EncodeWithout(encoded.(map[string]any)["message"], "message_id", "signature", "submission_receipt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(without, want) {
		t.Fatalf("EncodeWithout vector = %q; want %q", without, want)
	}
}

func TestKeyOrder(t *testing.T) {
	got, err := Canonicalize([]byte(`{"דּ":1,"😀":2,"€":3}`), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"€":3,"😀":2,"דּ":1}`; string(got) != want {
		t.Fatalf("Canonicalize key order = %q; want %q", got, want)
	}
}

func TestStringEscapes(t *testing.T) {
	input, err := hex.DecodeString("5b225c625c75303030625c75303031665c2f5c7530303766e280a8c3a9225d")
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString("5b225c625c75303030625c75303031662f7fe280a8c3a9225d")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Canonicalize(input, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Canonicalize(%q) = %q; want %q", input, got, want)
	}
}

func TestSurrogatePairs(t *testing.T) {
	t.Run("escaped pair for U+1F600 canonicalizes to raw UTF-8", func(t *testing.T) {
		got, err := Canonicalize([]byte(`"\ud83d\ude00"`), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if want := "\"\xf0\x9f\x98\x80\""; string(got) != want {
			t.Fatalf("Canonicalize = %q; want %q", got, want)
		}
	})
	t.Run("escaped pair parses to one code point", func(t *testing.T) {
		got, err := Parse([]byte(`"a\ud83d\ude00b"`), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		s, ok := got.(string)
		if !ok {
			t.Fatalf("Parse returned %T; want string", got)
		}
		if want := "a\U0001F600b"; s != want {
			t.Fatalf("Parse = %q; want %q", s, want)
		}
		if n := utf8.RuneCountInString(s); n != 3 {
			t.Fatalf("Parse yields %d code points; want 3", n)
		}
	})
	t.Run("high surrogate followed by escape that is not a low surrogate", func(t *testing.T) {
		if _, err := Parse([]byte(`"\ud83d\u0041"`), Limits{}); !errors.Is(err, ErrInvalidUnicode) {
			t.Fatalf("Parse error = %v; want ErrInvalidUnicode", err)
		}
	})
	t.Run("high surrogate followed by a high surrogate", func(t *testing.T) {
		if _, err := Parse([]byte(`"\ud83d\ud83d"`), Limits{}); !errors.Is(err, ErrInvalidUnicode) {
			t.Fatalf("Parse error = %v; want ErrInvalidUnicode", err)
		}
	})
}

func TestCanonicalNumbers(t *testing.T) {
	check := func(t *testing.T, input, want string) {
		t.Helper()
		got, err := Canonicalize([]byte(input), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("Canonicalize(%s) = %s; want %s", input, got, want)
		}
	}
	t.Run("one point zero", func(t *testing.T) { check(t, "1.0", "1") })
	t.Run("exponent integral", func(t *testing.T) { check(t, "1E2", "100") })
	t.Run("negative zero", func(t *testing.T) { check(t, "-0", "0") })
	t.Run("small decimal", func(t *testing.T) { check(t, "0.000001", "0.000001") })
	t.Run("small exponential", func(t *testing.T) { check(t, "1e-7", "1e-7") })
	t.Run("small exponential fraction", func(t *testing.T) { check(t, "2.5e-7", "2.5e-7") })
	t.Run("fraction", func(t *testing.T) { check(t, "1.5", "1.5") })
	t.Run("fractional digits", func(t *testing.T) { check(t, "123.456", "123.456") })
	t.Run("positive safe integer", func(t *testing.T) { check(t, "9007199254740991", "9007199254740991") })
	t.Run("negative safe integer", func(t *testing.T) { check(t, "-9007199254740991", "-9007199254740991") })
}

func TestNumberRejections(t *testing.T) {
	check := func(t *testing.T, input string) {
		t.Helper()
		if _, err := Parse([]byte(input), Limits{}); !errors.Is(err, ErrNumberRange) {
			t.Fatalf("Parse(%s) error = %v; want ErrNumberRange", input, err)
		}
	}
	t.Run("integer above safe range", func(t *testing.T) { check(t, "9007199254740992") })
	t.Run("large exponential", func(t *testing.T) { check(t, "1e16") })
	t.Run("decimal above safe range", func(t *testing.T) { check(t, "9007199254740992.0") })
	t.Run("float overflow", func(t *testing.T) { check(t, "1e400") })
	t.Run("Marshal rejects number outside the value model", func(t *testing.T) {
		if _, err := Marshal(json.Number("1.5e300")); !errors.Is(err, ErrNumberRange) {
			t.Fatalf("Marshal(1.5e300) error = %v; want ErrNumberRange", err)
		}
	})
}

func TestParserRejections(t *testing.T) {
	check := func(t *testing.T, input []byte, want error) {
		t.Helper()
		if _, err := Parse(input, Limits{}); !errors.Is(err, want) {
			t.Fatalf("Parse error = %v; want %v", err, want)
		}
	}
	t.Run("duplicate key after unescaping", func(t *testing.T) { check(t, []byte(`{"a":1,"\u0061":2}`), ErrDuplicateKey) })
	t.Run("lone high surrogate", func(t *testing.T) { check(t, []byte(`"\ud800"`), ErrInvalidUnicode) })
	t.Run("high surrogate before ordinary character", func(t *testing.T) { check(t, []byte(`"\ud800A"`), ErrInvalidUnicode) })
	t.Run("lone low surrogate", func(t *testing.T) { check(t, []byte(`"\udc00"`), ErrInvalidUnicode) })
	t.Run("invalid UTF-8 string", func(t *testing.T) { check(t, []byte{'"', 0xff, '"'}, ErrInvalidUnicode) })
	t.Run("BOM", func(t *testing.T) { check(t, []byte{0xef, 0xbb, 0xbf, '0'}, ErrSyntax) })
	t.Run("trailing text", func(t *testing.T) { check(t, []byte(`0x`), ErrSyntax) })
	t.Run("leading plus", func(t *testing.T) { check(t, []byte(`+1`), ErrSyntax) })
	t.Run("leading zero", func(t *testing.T) { check(t, []byte(`01`), ErrSyntax) })
	t.Run("leading decimal point", func(t *testing.T) { check(t, []byte(`.5`), ErrSyntax) })
	t.Run("trailing decimal point", func(t *testing.T) { check(t, []byte(`1.`), ErrSyntax) })
	t.Run("NaN", func(t *testing.T) { check(t, []byte(`NaN`), ErrSyntax) })
	t.Run("raw control character", func(t *testing.T) { check(t, []byte{'"', 0x01, '"'}, ErrSyntax) })
	t.Run("unknown escape", func(t *testing.T) { check(t, []byte(`"\q"`), ErrSyntax) })
}

func TestDepthLimits(t *testing.T) {
	depth32 := []byte(strings.Repeat("[", 32) + "0" + strings.Repeat("]", 32))
	depth33 := []byte(strings.Repeat("[", 33) + "0" + strings.Repeat("]", 33))
	t.Run("32 arrays accepted", func(t *testing.T) {
		if _, err := Parse(depth32, Limits{MaxDepth: 32}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("33 arrays rejected", func(t *testing.T) {
		if _, err := Parse(depth33, Limits{MaxDepth: 32}); !errors.Is(err, ErrTooDeep) {
			t.Fatalf("Parse error = %v; want ErrTooDeep", err)
		}
	})
}

func TestByteLimits(t *testing.T) {
	t.Run("16 bytes accepted", func(t *testing.T) {
		if _, err := Parse([]byte("               0"), Limits{MaxBytes: 16, MaxDepth: 32}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("17 bytes rejected before parse", func(t *testing.T) {
		if _, err := Parse([]byte("                0"), Limits{MaxBytes: 16, MaxDepth: 32}); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("Parse error = %v; want ErrTooLarge", err)
		}
	})
}

func TestEncode(t *testing.T) {
	type record struct {
		Text string `json:"text"`
	}
	t.Run("HTML characters remain raw", func(t *testing.T) {
		got, err := Encode(record{Text: "a<b&c"})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"text":"a<b&c"}` {
			t.Fatalf("Encode = %q", got)
		}
	})
	t.Run("invalid Go string rejected", func(t *testing.T) {
		if _, err := Encode(record{Text: "\xff"}); !errors.Is(err, ErrInvalidUnicode) {
			t.Fatalf("Encode error = %v; want ErrInvalidUnicode", err)
		}
	})
	t.Run("short shared slice does not hide invalid string", func(t *testing.T) {
		s := []any{"a", "\xff"}
		value := struct{ A, B []any }{s[:1], s}
		if _, err := Encode(value); !errors.Is(err, ErrInvalidUnicode) {
			t.Fatalf("Encode error = %v; want ErrInvalidUnicode", err)
		}
	})
	t.Run("unsupported model types rejected", func(t *testing.T) {
		if _, err := Marshal(1.0); err == nil {
			t.Fatal("Marshal(float64) succeeded")
		}
	})
}
