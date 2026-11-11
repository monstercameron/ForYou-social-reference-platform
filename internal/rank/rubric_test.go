package rank

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/wire"
)

var rubricTestNow = time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)

type rubricVector struct {
	Text       string
	Format     string
	Context    rubricVectorContext
	Fields     rubricVectorFields
	Indicators []string
	Floor      struct {
		Action  string
		Reasons []string
		Secrets []SecretMatch
	}
}

type rubricVectorContext struct {
	AuthorID      string `json:"author_id"`
	Named         bool
	RootSigned    *bool `json:"root_signed"`
	Settlement    string
	BillableBytes *int `json:"billable_bytes"`
	IsReply       bool `json:"is_reply"`
	SelfReply     bool `json:"self_reply"`
	Depth         int
	KnownReplies  int `json:"known_replies"`
	AgeHours      int `json:"age_hours"`
	Media         []Media
}

type rubricVectorFields struct {
	ProseChars     *int `json:"prose_chars"`
	Length         *Length
	Structured     *bool
	Readability    *float64
	ReadabilityNil bool `json:"readability_nil"`
	Images         *int
	Videos         *int
	AltCoverage    *float64 `json:"alt_coverage"`
	ActiveLinks    *int     `json:"active_links"`
	DistinctHosts  *int     `json:"distinct_hosts"`
	BareURLs       *int     `json:"bare_urls"`
	CodeBlock      *bool    `json:"code_block"`
	CodeSpan       *bool    `json:"code_span"`
	CodeRatio      *float64 `json:"code_ratio"`
	Secrets        []SecretMatch
	Profanity      *Profanity
	ProfanityNil   bool `json:"profanity_nil"`
	Threat         *float64
	Hate           *Hate
	Harass         *float64
	CapsRatio      *float64 `json:"caps_ratio"`
	BangRuns       *int     `json:"bang_runs"`
	Rep3           *float64
	CharRepeat     *float64 `json:"char_repeat"`
	Emoji          *int
	EmojiRatio     *float64 `json:"emoji_ratio"`
	Language       *string
	Script         *string
	Topics         []string
	PromoHits      *int  `json:"promo_hits"`
	SolicitSecret  *bool `json:"solicit_secret"`
	AdultLabel     *bool `json:"adult_label"`
	Named          *bool
	Delegate       *bool
	Settlement     *string
	KiBUnits       *int     `json:"kib_units"`
	AgeHours       *float64 `json:"age_hours"`
	Freshness      *float64
}

func rubricLoadVectors(t *testing.T) map[string]rubricVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/rubric-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var vectors map[string]rubricVector
	if err := decoder.Decode(&vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func rubricVectorPost(t *testing.T, vectors map[string]rubricVector, name string) (Post, rubricVector) {
	t.Helper()
	vector, ok := vectors[name]
	if !ok {
		t.Fatalf("missing vector %q", name)
	}
	format := vector.Format
	if format == "" {
		format = content.FormatPlain
	}
	segments, err := content.Segment(wire.PostPayload{ContentFormat: format, Text: vector.Text})
	if err != nil {
		t.Fatal(err)
	}
	c := vector.Context
	post := Post{
		ID:            name,
		AuthorID:      "C",
		RootSigned:    true,
		Segments:      segments,
		Media:         c.Media,
		BillableBytes: 100,
		Named:         c.Named,
		Settlement:    "settled",
		IsReply:       c.IsReply,
		SelfReply:     c.SelfReply,
		Depth:         c.Depth,
		KnownReplies:  c.KnownReplies,
		Timestamp:     rubricTestNow.Add(-time.Duration(c.AgeHours) * time.Hour),
	}
	if c.AuthorID != "" {
		post.AuthorID = c.AuthorID
	}
	if c.RootSigned != nil {
		post.RootSigned = *c.RootSigned
	}
	if c.Settlement != "" {
		post.Settlement = c.Settlement
	}
	if c.BillableBytes != nil {
		post.BillableBytes = *c.BillableBytes
	}
	return post, vector
}

func TestRubricVectors(t *testing.T) {
	vectors := rubricLoadVectors(t)
	check := func(t *testing.T, name string) {
		t.Helper()
		post, vector := rubricVectorPost(t, vectors, name)
		f := Extract(post, rubricTestNow)
		if f.Rubric != "fy-rubric-1" || f.PostID != name {
			t.Errorf("Rubric, PostID = %q, %q; want %q, %q", f.Rubric, f.PostID, "fy-rubric-1", name)
		}
		rubricCheckFields(t, f, vector.Fields)
		if got := f.Indicators(); !reflect.DeepEqual(got, vector.Indicators) {
			t.Errorf("Indicators() = %q; want %q", got, vector.Indicators)
		}
		actions := map[string]FloorAction{"none": FloorNone, "collapse": FloorCollapse, "hide": FloorHide}
		action, ok := actions[vector.Floor.Action]
		if !ok {
			t.Fatalf("unknown floor action %q", vector.Floor.Action)
		}
		got := f.Floor()
		if got.Action != action || !reflect.DeepEqual(got.Reasons, vector.Floor.Reasons) || !reflect.DeepEqual(got.Secrets, vector.Floor.Secrets) {
			t.Errorf("Floor() = %+v; want {%d %q %v}", got, action, vector.Floor.Reasons, vector.Floor.Secrets)
		}
	}
	t.Run("p1-useful-answer", func(t *testing.T) { check(t, "p1-useful-answer") })
	t.Run("p2-profanity", func(t *testing.T) { check(t, "p2-profanity") })
	t.Run("p3-threat", func(t *testing.T) { check(t, "p3-threat") })
	t.Run("p4-leaked-key", func(t *testing.T) { check(t, "p4-leaked-key") })
	t.Run("p5-dehumanizing", func(t *testing.T) { check(t, "p5-dehumanizing") })
	t.Run("p6-spam", func(t *testing.T) { check(t, "p6-spam") })
	t.Run("p7-deep-chatter", func(t *testing.T) { check(t, "p7-deep-chatter") })
	t.Run("p8-shouted-insult", func(t *testing.T) { check(t, "p8-shouted-insult") })
	t.Run("scam-fixture", func(t *testing.T) { check(t, "scam-fixture") })
	t.Run("exporter-fixture", func(t *testing.T) { check(t, "exporter-fixture") })
	t.Run("kick-fixture", func(t *testing.T) { check(t, "kick-fixture") })
	t.Run("pathetic-fixture", func(t *testing.T) { check(t, "pathetic-fixture") })
	t.Run("nsfw-label", func(t *testing.T) { check(t, "nsfw-label") })
	t.Run("extreme-spam", func(t *testing.T) { check(t, "extreme-spam") })
	t.Run("release-notes", func(t *testing.T) { check(t, "release-notes") })
	t.Run("dense-prose", func(t *testing.T) { check(t, "dense-prose") })
	t.Run("spanish", func(t *testing.T) { check(t, "spanish") })
	t.Run("russian", func(t *testing.T) { check(t, "russian") })
	t.Run("japanese", func(t *testing.T) { check(t, "japanese") })
	t.Run("context", func(t *testing.T) { check(t, "context") })
	t.Run("inline-code", func(t *testing.T) { check(t, "inline-code") })
	t.Run("three-paragraphs", func(t *testing.T) { check(t, "three-paragraphs") })
	t.Run("emoji-spam", func(t *testing.T) { check(t, "emoji-spam") })
	t.Run("punct-spam", func(t *testing.T) { check(t, "punct-spam") })
}

func rubricCheckFields(t *testing.T, f Features, want rubricVectorFields) {
	t.Helper()
	rubricWant(t, "ProseChars", f.ProseChars, want.ProseChars)
	rubricWant(t, "Length", f.Length, want.Length)
	rubricWant(t, "Structured", f.Structured, want.Structured)
	rubricWantOptional(t, "Readability", f.Readability, want.Readability, want.ReadabilityNil)
	rubricWant(t, "Images", f.Images, want.Images)
	rubricWant(t, "Videos", f.Videos, want.Videos)
	rubricWantOptional(t, "AltCoverage", f.AltCoverage, want.AltCoverage, false)
	rubricWant(t, "ActiveLinks", f.ActiveLinks, want.ActiveLinks)
	rubricWant(t, "DistinctHosts", f.DistinctHosts, want.DistinctHosts)
	rubricWant(t, "BareURLs", f.BareURLs, want.BareURLs)
	rubricWant(t, "CodeBlock", f.CodeBlock, want.CodeBlock)
	rubricWant(t, "CodeSpan", f.CodeSpan, want.CodeSpan)
	rubricWantFloat(t, "CodeRatio", f.CodeRatio, want.CodeRatio)
	if want.Secrets != nil && !reflect.DeepEqual(f.Secrets, want.Secrets) {
		t.Errorf("Secrets = %v; want %v", f.Secrets, want.Secrets)
	}
	rubricWantProfanity(t, f.Profanity, want.Profanity, want.ProfanityNil)
	rubricWantFloat(t, "Threat", f.Threat, want.Threat)
	rubricWant(t, "Hate", f.Hate, want.Hate)
	rubricWantFloat(t, "Harass", f.Harass, want.Harass)
	rubricWantFloat(t, "CapsRatio", f.CapsRatio, want.CapsRatio)
	rubricWant(t, "BangRuns", f.BangRuns, want.BangRuns)
	rubricWantFloat(t, "Rep3", f.Repetition.Rep3, want.Rep3)
	rubricWantFloat(t, "CharRepeat", f.Repetition.CharRepeat, want.CharRepeat)
	rubricWant(t, "Emoji", f.Repetition.Emoji, want.Emoji)
	rubricWantFloat(t, "EmojiRatio", f.Repetition.EmojiRatio, want.EmojiRatio)
	rubricWant(t, "Language", f.Language, want.Language)
	rubricWant(t, "Script", f.Script, want.Script)
	if want.Topics != nil && !reflect.DeepEqual(f.Topics, want.Topics) {
		t.Errorf("Topics = %q; want %q", f.Topics, want.Topics)
	}
	rubricWant(t, "PromoHits", f.PromoHits, want.PromoHits)
	rubricWant(t, "SolicitSecret", f.SolicitSecret, want.SolicitSecret)
	rubricWant(t, "AdultLabel", f.AdultLabel, want.AdultLabel)
	rubricWant(t, "Named", f.Named, want.Named)
	rubricWant(t, "Delegate", f.Delegate, want.Delegate)
	rubricWant(t, "Settlement", f.Settlement, want.Settlement)
	rubricWant(t, "KiBUnits", f.KiBUnits, want.KiBUnits)
	rubricWantFloat(t, "AgeHours", f.AgeHours, want.AgeHours)
	rubricWantFloat(t, "Freshness", f.Freshness, want.Freshness)
}

func rubricWant[T comparable](t *testing.T, field string, got T, want *T) {
	t.Helper()
	if want != nil && got != *want {
		t.Errorf("%s = %v; want %v", field, got, *want)
	}
}

func rubricWantFloat(t *testing.T, field string, got float64, want *float64) {
	t.Helper()
	if want != nil && math.Abs(got-*want) > 1e-6 {
		t.Errorf("%s = %.9f; want %.9f", field, got, *want)
	}
}

func rubricWantOptional(t *testing.T, field string, got, want *float64, wantNil bool) {
	t.Helper()
	switch {
	case wantNil && got != nil:
		t.Errorf("%s = %v; want nil", field, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil; want %v", field, *want)
	case want != nil:
		rubricWantFloat(t, field, *got, want)
	}
}

func rubricWantProfanity(t *testing.T, got, want *Profanity, wantNil bool) {
	t.Helper()
	switch {
	case wantNil && got != nil:
		t.Errorf("Profanity = %+v; want nil", *got)
	case want != nil && got == nil:
		t.Errorf("Profanity = nil; want %+v", *want)
	case want != nil:
		if got.Mild != want.Mild || got.Strong != want.Strong || math.Abs(got.Rate-want.Rate) > 1e-6 {
			t.Errorf("Profanity = %+v; want %+v", *got, *want)
		}
	}
}

func TestRubricLengthBuckets(t *testing.T) {
	check := func(t *testing.T, size int, want Length) {
		t.Helper()
		segments, err := content.Segment(wire.PostPayload{ContentFormat: "text/plain", Text: strings.Repeat("a", size)})
		if err != nil {
			t.Fatal(err)
		}
		if got := Extract(Post{Segments: segments}, time.Time{}).Length; got != want {
			t.Errorf("Length for %d chars = %q; want %q", size, got, want)
		}
	}
	t.Run("39 tiny", func(t *testing.T) { check(t, 39, LengthTiny) })
	t.Run("40 short", func(t *testing.T) { check(t, 40, LengthShort) })
	t.Run("279 short", func(t *testing.T) { check(t, 279, LengthShort) })
	t.Run("280 medium", func(t *testing.T) { check(t, 280, LengthMedium) })
	t.Run("999 medium", func(t *testing.T) { check(t, 999, LengthMedium) })
	t.Run("1000 long", func(t *testing.T) { check(t, 1000, LengthLong) })
}

func TestRubricIndicatorThresholds(t *testing.T) {
	check := func(t *testing.T, features Features, want ...string) {
		t.Helper()
		if got := features.Indicators(); !reflect.DeepEqual(got, want) {
			t.Errorf("Indicators() = %v; want %v", got, want)
		}
	}
	base := Features{Length: LengthTiny, Language: "und", AuthorID: "C"}
	t.Run("readability boundary", func(t *testing.T) {
		value := 0.5
		item := base
		item.Readability = &value
		check(t, item, "key:C", "lang:und", "len:tiny")
	})
	t.Run("dense readability", func(t *testing.T) {
		value := 0.49
		item := base
		item.Readability = &value
		check(t, item, "key:C", "lang:und", "len:tiny", "read:dense")
	})
	t.Run("two links", func(t *testing.T) {
		item := base
		item.ActiveLinks = 2
		check(t, item, "key:C", "lang:und", "len:tiny", "link:any")
	})
	t.Run("three links", func(t *testing.T) {
		item := base
		item.ActiveLinks = 3
		check(t, item, "key:C", "lang:und", "len:tiny", "link:any", "link:many")
	})
	t.Run("caps below threshold", func(t *testing.T) {
		item := base
		item.CapsRatio = 0.69
		check(t, item, "key:C", "lang:und", "len:tiny")
	})
	t.Run("caps threshold", func(t *testing.T) {
		item := base
		item.CapsRatio = 0.7
		check(t, item, "key:C", "lang:und", "len:tiny", "spam:caps")
	})
	t.Run("trigram repeat", func(t *testing.T) {
		item := base
		item.Repetition.Rep3 = 0.4
		check(t, item, "key:C", "lang:und", "len:tiny", "spam:repeat")
	})
	t.Run("character repeat", func(t *testing.T) {
		item := base
		item.Repetition.CharRepeat = 0.5
		check(t, item, "key:C", "lang:und", "len:tiny", "spam:repeat")
	})
	t.Run("one punctuation run", func(t *testing.T) {
		item := base
		item.BangRuns = 1
		check(t, item, "key:C", "lang:und", "len:tiny")
	})
	t.Run("two punctuation runs", func(t *testing.T) {
		item := base
		item.BangRuns = 2
		check(t, item, "key:C", "lang:und", "len:tiny", "spam:punct")
	})
	t.Run("five emoji", func(t *testing.T) {
		item := base
		item.Repetition.Emoji, item.Repetition.EmojiRatio = 5, 0.9
		check(t, item, "key:C", "lang:und", "len:tiny")
	})
	t.Run("six emoji", func(t *testing.T) {
		item := base
		item.Repetition.Emoji, item.Repetition.EmojiRatio = 6, 0.3
		check(t, item, "key:C", "lang:und", "len:tiny", "spam:emoji")
	})
	t.Run("harassment threshold", func(t *testing.T) {
		item := base
		item.Harass = 0.5
		check(t, item, "harass", "key:C", "lang:und", "len:tiny")
	})
	t.Run("depth below threshold", func(t *testing.T) {
		item := base
		item.Depth = 3
		check(t, item, "key:C", "lang:und", "len:tiny")
	})
	t.Run("deep thread", func(t *testing.T) {
		item := base
		item.Depth = 4
		check(t, item, "key:C", "lang:und", "len:tiny", "thread:deep")
	})
	t.Run("busy thread", func(t *testing.T) {
		item := base
		item.KnownReplies = 4
		check(t, item, "key:C", "lang:und", "len:tiny", "thread:busy")
	})
	t.Run("profanity nil", func(t *testing.T) {
		check(t, base, "key:C", "lang:und", "len:tiny")
	})
	t.Run("empty script", func(t *testing.T) {
		item := base
		item.Script = ""
		check(t, item, "key:C", "lang:und", "len:tiny")
	})
	t.Run("latin script", func(t *testing.T) {
		item := base
		item.Script = "latin"
		check(t, item, "key:C", "lang:und", "len:tiny")
	})
	t.Run("safety fields do not become indicators", func(t *testing.T) {
		item := base
		item.Threat, item.Hate.Dehumanizing, item.SolicitSecret, item.Secrets = 1, true, true, []SecretMatch{{Kind: "token", Text: "x"}}
		check(t, item, "key:C", "lang:und", "len:tiny")
	})
}

func TestRubricFloorRows(t *testing.T) {
	check := func(t *testing.T, features Features, action FloorAction, reasons []string, secrets []SecretMatch) {
		t.Helper()
		got := features.Floor()
		if got.Action != action || !reflect.DeepEqual(got.Reasons, reasons) || !reflect.DeepEqual(got.Secrets, secrets) {
			t.Errorf("Floor() = %+v; want action %d, reasons %v, secrets %v", got, action, reasons, secrets)
		}
	}
	t.Run("direct threat", func(t *testing.T) { check(t, Features{Threat: 1}, FloorHide, []string{"threat"}, nil) })
	t.Run("directed hate", func(t *testing.T) {
		check(t, Features{Hate: Hate{Slurs: 1, Directed: true}}, FloorHide, []string{"hate:directed"}, nil)
	})
	t.Run("dehumanizing hate", func(t *testing.T) {
		check(t, Features{Hate: Hate{Dehumanizing: true}}, FloorHide, []string{"hate:dehumanizing"}, nil)
	})
	t.Run("solicited secret", func(t *testing.T) {
		check(t, Features{SolicitSecret: true}, FloorHide, []string{"solicit_secret"}, nil)
	})
	t.Run("secrets sorted and copied", func(t *testing.T) {
		secrets := []SecretMatch{{Kind: "aws_access_key_id", Text: "X"}, {Kind: "assigned_secret", Text: "Y"}}
		check(t, Features{Secrets: secrets}, FloorCollapse, []string{"secret:assigned_secret", "secret:aws_access_key_id"}, secrets)
	})
	t.Run("indirect threat", func(t *testing.T) { check(t, Features{Threat: 0.5}, FloorCollapse, []string{"threat:indirect"}, nil) })
	t.Run("undirected slur", func(t *testing.T) {
		check(t, Features{Hate: Hate{Slurs: 2}}, FloorCollapse, []string{"hate:slur"}, nil)
	})
	t.Run("harassment", func(t *testing.T) { check(t, Features{Harass: 0.5}, FloorCollapse, []string{"harass"}, nil) })
	t.Run("adult label", func(t *testing.T) { check(t, Features{AdultLabel: true}, FloorCollapse, []string{"adult"}, nil) })
	t.Run("extreme spam", func(t *testing.T) {
		check(t, Features{CapsRatio: 0.7, Repetition: Repetition{Rep3: 0.4}}, FloorCollapse, []string{"spam:extreme"}, nil)
	})
	t.Run("caps below extreme threshold", func(t *testing.T) {
		check(t, Features{CapsRatio: 0.69, Repetition: Repetition{Rep3: 0.4}}, FloorNone, nil, nil)
	})
	t.Run("no floor", func(t *testing.T) { check(t, Features{}, FloorNone, nil, nil) })
	t.Run("secret matches are copied", func(t *testing.T) {
		features := Features{Secrets: []SecretMatch{{Kind: "credential", Text: "secret"}}}
		result := features.Floor()
		features.Secrets[0].Text = "changed"
		if result.Secrets[0].Text != "secret" {
			t.Errorf("Floor() Secrets changed with input: %v", result.Secrets)
		}
	})
}

func TestRubricFloorOrder(t *testing.T) {
	features := Features{Threat: 1, Secrets: []SecretMatch{{Kind: "aws_access_key_id", Text: "X"}}, AdultLabel: true}
	got := features.Floor()
	wantReasons := []string{"threat", "secret:aws_access_key_id", "adult"}
	wantSecrets := []SecretMatch{{Kind: "aws_access_key_id", Text: "X"}}
	if got.Action != FloorHide || !reflect.DeepEqual(got.Reasons, wantReasons) || !reflect.DeepEqual(got.Secrets, wantSecrets) {
		t.Errorf("Floor() = %+v; want hide, %v, %v", got, wantReasons, wantSecrets)
	}
}

func TestRubricDeterminism(t *testing.T) {
	post, _ := rubricVectorPost(t, rubricLoadVectors(t), "p1-useful-answer")
	first := Extract(post, rubricTestNow)
	second := Extract(post, rubricTestNow)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Extract is not deterministic: %+v != %+v", first, second)
	}
}
