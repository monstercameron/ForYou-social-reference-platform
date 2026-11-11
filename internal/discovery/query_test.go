package discovery

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseListQueryDateNormalizationAndMatch(t *testing.T) {
	day, _, err := ParseListQuery(Messages, "", "date=2024-02-29")
	if err != nil {
		t.Fatal(err)
	}
	explicit, _, err := ParseListQuery(Messages, "", "from=2024-02-29T00%3A00%3A00Z&until=2024-03-01T00%3A00%3A00Z&limit=50")
	if err != nil {
		t.Fatal(err)
	}
	if day.Binding() != explicit.Binding() {
		t.Fatalf("date and equivalent range bindings differ: %q != %q", day.Binding(), explicit.Binding())
	}
	checkMatch := func(t *testing.T, stamp string, want bool) {
		t.Helper()
		at, err := parseTimestamp(stamp)
		if err != nil {
			t.Fatal(err)
		}
		if got := day.Match(Record{Timestamp: at}); got != want {
			t.Errorf("Match(%s) = %t; want %t", stamp, got, want)
		}
	}
	t.Run("before lower bound", func(t *testing.T) { checkMatch(t, "2024-02-28T23:59:59Z", false) })
	t.Run("at lower bound", func(t *testing.T) { checkMatch(t, "2024-02-29T00:00:00Z", true) })
	t.Run("before upper bound", func(t *testing.T) { checkMatch(t, "2024-02-29T23:59:59Z", true) })
	t.Run("at upper bound", func(t *testing.T) { checkMatch(t, "2024-03-01T00:00:00Z", false) })
}

func TestQueryBindingDistinguishesEffectiveFilters(t *testing.T) {
	date, _, err := ParseListQuery(Messages, "", "date=2024-02-29")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("different limits differ", func(t *testing.T) {
		limited, _, err := ParseListQuery(Messages, "", "date=2024-02-29&limit=49")
		if err != nil {
			t.Fatal(err)
		}
		if date.Binding() == limited.Binding() {
			t.Fatal("different effective limits have equal bindings")
		}
	})
	t.Run("different routes differ", func(t *testing.T) {
		target := "fy1:" + strings.Repeat("a", 64)
		replies, _, err := ParseListQuery(Replies, target, "")
		if err != nil {
			t.Fatal(err)
		}
		versions, _, err := ParseListQuery(Versions, target, "")
		if err != nil {
			t.Fatal(err)
		}
		if replies.Binding() == versions.Binding() {
			t.Fatal("different routes have equal bindings")
		}
	})
	t.Run("different targets differ", func(t *testing.T) {
		first, _, err := ParseListQuery(Replies, "fy1:"+strings.Repeat("a", 64), "")
		if err != nil {
			t.Fatal(err)
		}
		second, _, err := ParseListQuery(Replies, "fy1:"+strings.Repeat("b", 64), "")
		if err != nil {
			t.Fatal(err)
		}
		if first.Binding() == second.Binding() {
			t.Fatal("different targets have equal bindings")
		}
	})
}

func TestParseListQueryTimestampBounds(t *testing.T) {
	t.Run("year one timestamp is a present bound", func(t *testing.T) {
		query, _, err := ParseListQuery(Messages, "", "from=0001-01-01T00%3A00%3A00Z")
		if err != nil {
			t.Fatal(err)
		}
		absent, _, err := ParseListQuery(Messages, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if query.Binding() == absent.Binding() {
			t.Fatal("explicit year-one bound matched the absent-bound binding")
		}
		if !query.Match(Record{Timestamp: query.From}) {
			t.Fatal("year-one timestamp did not match its inclusive lower bound")
		}
	})
	t.Run("last representable day lower bound", func(t *testing.T) {
		query, _, err := ParseListQuery(Messages, "", "from=9999-12-31T00%3A00%3A00Z")
		if err != nil {
			t.Fatal(err)
		}
		at, _ := parseTimestamp("9999-12-31T23:59:59Z")
		if !query.Match(Record{Timestamp: at}) {
			t.Fatal("last representable second did not match")
		}
	})
	t.Run("spring day range", func(t *testing.T) {
		checkRangeDuration(t, "2024-03-10T05%3A00%3A00Z", "2024-03-11T04%3A00%3A00Z", 23*time.Hour)
	})
	t.Run("autumn day range", func(t *testing.T) {
		checkRangeDuration(t, "2024-11-03T04%3A00%3A00Z", "2024-11-04T05%3A00%3A00Z", 25*time.Hour)
	})
}

func TestParseListQueryInvalidQuery(t *testing.T) {
	check := func(t *testing.T, raw string, route Route) {
		t.Helper()
		_, _, err := ParseListQuery(route, "", raw)
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("ParseListQuery(%q) error = %v; want invalid_query", raw, err)
		}
	}
	t.Run("invalid leap day", func(t *testing.T) { check(t, "date=2026-02-29", Messages) })
	t.Run("invalid calendar day", func(t *testing.T) { check(t, "date=2024-02-30", Messages) })
	t.Run("zero year", func(t *testing.T) { check(t, "date=0000-01-01", Messages) })
	t.Run("unrepresentable next midnight", func(t *testing.T) { check(t, "date=9999-12-31", Messages) })
	t.Run("date with from", func(t *testing.T) { check(t, "date=2026-11-10&from=2026-11-10T00%3A00%3A00Z", Messages) })
	t.Run("leading zero limit", func(t *testing.T) { check(t, "limit=01", Messages) })
	t.Run("limit above maximum", func(t *testing.T) { check(t, "limit=101", Messages) })
	t.Run("zero limit", func(t *testing.T) { check(t, "limit=0", Messages) })
	t.Run("repeated date", func(t *testing.T) { check(t, "date=2026-11-10&date=2026-11-11", Messages) })
	t.Run("escaped parameter name", func(t *testing.T) { check(t, "%64ate=2026-11-10", Messages) })
	t.Run("empty date", func(t *testing.T) { check(t, "date=", Messages) })
	t.Run("trailing empty component", func(t *testing.T) { check(t, "date=2026-11-10&", Messages) })
	t.Run("raw colon in timestamp", func(t *testing.T) { check(t, "from=2026-11-10T00:00:00Z", Messages) })
	t.Run("invalid UTF-8 cursor", func(t *testing.T) { check(t, "cursor=%FF", Messages) })
	t.Run("lowercase percent hex", func(t *testing.T) { check(t, "date=2026%2d11%2d10", Messages) })
	t.Run("plus is not space", func(t *testing.T) { check(t, "limit=+1", Messages) })
	t.Run("double encoded value", func(t *testing.T) { check(t, "date=2026%252D11%252D10", Messages) })
	t.Run("unknown timezone parameter", func(t *testing.T) { check(t, "timezone=UTC", Messages) })
	t.Run("unknown quota parameter", func(t *testing.T) { check(t, "quota_mode=skip", Messages) })
	t.Run("date used as timestamp", func(t *testing.T) { check(t, "from=2026-11-10", Messages) })
	t.Run("offset timestamp", func(t *testing.T) { check(t, "from=2026-11-10T00%3A00%3A00%2B00%3A00", Messages) })
	t.Run("fractional timestamp", func(t *testing.T) { check(t, "from=2026-11-10T00%3A00%3A00.1Z", Messages) })
	t.Run("leap second", func(t *testing.T) { check(t, "from=2026-11-10T23%3A59%3A60Z", Messages) })
	t.Run("equal interval bounds", func(t *testing.T) { check(t, "from=2026-11-10T00%3A00%3A00Z&until=2026-11-10T00%3A00%3A00Z", Messages) })
	t.Run("reversed interval bounds", func(t *testing.T) { check(t, "from=2026-11-11T00%3A00%3A00Z&until=2026-11-10T00%3A00%3A00Z", Messages) })
	t.Run("oversized raw query", func(t *testing.T) { check(t, strings.Repeat("a", MaxRawQueryBytes+1), Messages) })
	t.Run("author rejected on message route", func(t *testing.T) {
		check(t, "author_id=did%3Akey%3Az6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd", Messages)
	})
	t.Run("required author missing", func(t *testing.T) { check(t, "", AuthorMessages) })
	t.Run("malformed raw component", func(t *testing.T) { check(t, "date", Messages) })
	t.Run("leading empty component", func(t *testing.T) { check(t, "&date=2026-11-10", Messages) })
}

func TestParseListQueryCursorPrecedence(t *testing.T) {
	checkInvalidCursor := func(t *testing.T, value string) {
		t.Helper()
		_, _, err := ParseListQuery(Messages, "", "cursor="+value)
		if !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor %q error = %v; want invalid_cursor", value, err)
		}
	}
	t.Run("empty cursor", func(t *testing.T) { checkInvalidCursor(t, "") })
	t.Run("short cursor", func(t *testing.T) { checkInvalidCursor(t, "short") })
	t.Run("nonzero trailing bits", func(t *testing.T) { checkInvalidCursor(t, strings.Repeat("A", 42)+"B") })
	t.Run("encoded padding", func(t *testing.T) { checkInvalidCursor(t, strings.Repeat("A", 43)+"%3D") })
	t.Run("invalid alphabet", func(t *testing.T) { checkInvalidCursor(t, strings.Repeat("%21", 43)) })
	t.Run("invalid value wins first", func(t *testing.T) {
		_, _, err := ParseListQuery(Messages, "", "cursor=%FF")
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error = %v; want invalid_query", err)
		}
	})
	t.Run("duplicate wins first", func(t *testing.T) {
		_, _, err := ParseListQuery(Messages, "", "cursor=a&cursor=b")
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error = %v; want invalid_query", err)
		}
	})
	t.Run("filter wins first", func(t *testing.T) {
		_, _, err := ParseListQuery(Messages, "", "date=2026-02-30&cursor=short")
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error = %v; want invalid_query", err)
		}
	})
}

func TestParseTargetCanonicalPathEncoding(t *testing.T) {
	message := "fy1:" + strings.Repeat("a", 64)
	t.Run("message target", func(t *testing.T) {
		got, err := ParseTarget(Replies, "fy1%3A"+strings.Repeat("a", 64))
		if err != nil || got != message {
			t.Fatalf("ParseTarget() = %q, %v; want %q", got, err, message)
		}
	})
	t.Run("raw colon rejected", func(t *testing.T) {
		if _, err := ParseTarget(Replies, message); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error = %v; want invalid_query", err)
		}
	})
	t.Run("lowercase escape rejected", func(t *testing.T) {
		if _, err := ParseTarget(Replies, "fy1%3a"+strings.Repeat("a", 64)); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error = %v; want invalid_query", err)
		}
	})
	did := "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"
	t.Run("DID reference target", func(t *testing.T) {
		got, err := ParseTarget(References, "did%3Akey%3Az6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd")
		if err != nil || got != did {
			t.Fatalf("ParseTarget() = %q, %v; want %q", got, err, did)
		}
	})
	t.Run("DID rejected for versions", func(t *testing.T) {
		if _, err := ParseTarget(Versions, "did%3Akey%3Az6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error = %v; want invalid_query", err)
		}
	})
	t.Run("lowercase DID escape rejected", func(t *testing.T) {
		if _, err := ParseTarget(References, "did%3akey%3Az6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error = %v; want invalid_query", err)
		}
	})
}

func TestParseChangesQuery(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		after, limit, err := ParseChangesQuery("")
		if err != nil || after != 0 || limit != DefaultLimit {
			t.Fatalf("ParseChangesQuery(empty) = (%d, %d, %v); want (0, 50, nil)", after, limit, err)
		}
	})
	t.Run("maximum safe after", func(t *testing.T) {
		after, limit, err := ParseChangesQuery("after=9007199254740991&limit=100")
		if err != nil || after != MaxAfter || limit != MaxLimit {
			t.Fatalf("ParseChangesQuery() = (%d, %d, %v); want (%d, 100, nil)", after, limit, err, MaxAfter)
		}
	})
	checkInvalid := func(t *testing.T, raw string) {
		t.Helper()
		if _, _, err := ParseChangesQuery(raw); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("ParseChangesQuery(%q) error = %v; want invalid_query", raw, err)
		}
	}
	t.Run("above maximum", func(t *testing.T) { checkInvalid(t, "after=9007199254740992") })
	t.Run("leading zero", func(t *testing.T) { checkInvalid(t, "after=01") })
	t.Run("negative after", func(t *testing.T) { checkInvalid(t, "after=-1") })
	t.Run("zero limit", func(t *testing.T) { checkInvalid(t, "limit=0") })
	t.Run("unsupported cursor", func(t *testing.T) { checkInvalid(t, "cursor=x") })
}

func checkRangeDuration(t *testing.T, from, until string, want time.Duration) {
	t.Helper()
	query, _, err := ParseListQuery(Messages, "", "from="+from+"&until="+until)
	if err != nil {
		t.Fatal(err)
	}
	if got := query.Until.Sub(query.From); got != want {
		t.Fatalf("range duration = %v; want %v", got, want)
	}
}
