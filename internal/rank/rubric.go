package rank

import (
	"math"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	"foryou.local/foryou/internal/content"
)

// RubricVersion identifies the objective feature extraction rules.
const RubricVersion = "fy-rubric-1"

// Post is a verified record with the public context the rubric may use.
type Post struct {
	ID, AuthorID        string
	RootSigned          bool // false when a delegate key signed
	Segments            content.Segments
	Media               []Media
	BillableBytes       int
	Named               bool   // a verified current NameRecord with a non-null name
	Settlement          string // "settled" or "pending_settlement"
	IsReply, SelfReply  bool
	Depth, KnownReplies int // Depth −1 when the parent is unresolved
	Timestamp           time.Time
}

// Media is one media slot's metadata; bytes are never fetched.
type Media struct {
	Kind      string // "image" or "video"
	HasAlt    bool
	SizeBytes int64
}

// Length is rubric R2.
type Length string

const (
	// LengthTiny identifies prose shorter than 40 Unicode scalars.
	LengthTiny Length = "tiny"
	// LengthShort identifies prose from 40 through 279 Unicode scalars.
	LengthShort Length = "short"
	// LengthMedium identifies prose from 280 through 999 Unicode scalars.
	LengthMedium Length = "medium"
	// LengthLong identifies prose with at least 1000 Unicode scalars.
	LengthLong Length = "long"
)

// Profanity is rubric R9.
type Profanity struct {
	Mild, Strong int
	Rate         float64
}

// Features is the rubric vector R1–R24 of one post.
type Features struct {
	Rubric, PostID, AuthorID string
	ProseChars               int           // R1
	Length                   Length        // R2
	Structured               bool          // R3
	Readability              *float64      // R4
	Images, Videos           int           // R5
	AltCoverage              *float64      // R5
	ActiveLinks              int           // R6
	DistinctHosts, BareURLs  int           // R6
	CodeBlock, CodeSpan      bool          // R7
	CodeRatio                float64       // R7
	Secrets                  []SecretMatch // R8
	Profanity                *Profanity    // R9
	Threat                   float64       // R10
	Hate                     Hate          // R11
	Harass                   float64       // R12
	CapsRatio                float64       // R13
	BangRuns                 int           // R13
	Repetition               Repetition    // R14
	Language, Script         string        // R15, R16
	Topics                   []string      // R17
	PromoHits                int           // R18
	SolicitSecret            bool          // R18
	AdultLabel               bool          // R19
	Named, Delegate          bool          // R20
	Settlement               string        // R21
	BillableBytes, KiBUnits  int           // R22
	IsReply, SelfReply       bool          // R23
	Depth, KnownReplies      int           // R23
	AgeHours, Freshness      float64       // R24
}

var rubricBareURLPattern = regexp.MustCompile(`(?i)\bhttps?://\S+`)

// Extract returns the deterministic rubric features for p at now.
func Extract(p Post, now time.Time) Features {
	prose := p.Segments.Prose
	text := textAnalyze(prose)
	words := text.words
	features := Features{
		Rubric:        RubricVersion,
		PostID:        p.ID,
		AuthorID:      p.AuthorID,
		ProseChars:    rubricProseRunes(prose),
		Structured:    p.Segments.Structure.Lists+p.Segments.Structure.Headings+p.Segments.Structure.Quotes >= 1 || p.Segments.Structure.Paragraphs >= 3,
		CodeBlock:     len(p.Segments.CodeBlocks) > 0,
		CodeSpan:      len(p.Segments.CodeSpans) > 0,
		Threat:        safetyThreat(text),
		Hate:          safetyHate(text),
		Harass:        safetyHarass(text),
		Repetition:    styleRepetition(prose, words),
		Language:      langLanguage(words),
		Script:        langScript(prose),
		AdultLabel:    safetyAdult(prose, text),
		Named:         p.Named,
		Delegate:      !p.RootSigned,
		Settlement:    p.Settlement,
		BillableBytes: p.BillableBytes,
		IsReply:       p.IsReply,
		SelfReply:     p.SelfReply,
		Depth:         p.Depth,
		KnownReplies:  p.KnownReplies,
	}
	features.Length = rubricLength(features.ProseChars)
	features.Readability = rubricReadability(text, features.Script)
	features.Images, features.Videos, features.AltCoverage = rubricMedia(p.Media)
	features.ActiveLinks, features.DistinctHosts, features.BareURLs = rubricLinks(p.Segments)
	codeRunes := rubricProseRunes(p.Segments.CodeBlocks) + rubricProseRunes(p.Segments.CodeSpans)
	if total := codeRunes + features.ProseChars; total > 0 {
		features.CodeRatio = float64(codeRunes) / float64(total)
	}
	secretTexts := make([]string, 0, len(prose)+len(p.Segments.CodeBlocks)+len(p.Segments.CodeSpans))
	secretTexts = append(secretTexts, prose...)
	secretTexts = append(secretTexts, p.Segments.CodeBlocks...)
	secretTexts = append(secretTexts, p.Segments.CodeSpans...)
	features.Secrets = safetySecrets(secretTexts)
	if features.Language != "es" && features.Language != "fr" && features.Language != "de" && features.Language != "pt" && features.Language != "it" && features.Language != "nl" {
		mild, strong := safetyProfanity(text)
		rate := 0.0
		if len(words) > 0 {
			rate = float64(mild+2*strong) / float64(len(words))
		}
		features.Profanity = &Profanity{Mild: mild, Strong: strong, Rate: rate}
	}
	features.CapsRatio, features.BangRuns = styleShouting(prose)
	features.Topics = langTopics(words, features.CodeBlock)
	features.PromoHits, features.SolicitSecret = safetyPromo(text)
	if p.BillableBytes > 0 {
		features.KiBUnits = 1 + (p.BillableBytes-1)/1024
	} else {
		features.KiBUnits = p.BillableBytes / 1024
	}
	features.AgeHours = math.Max(0, now.Sub(p.Timestamp).Hours())
	features.Freshness = math.Pow(2, -features.AgeHours/24)
	return features
}

func rubricReadability(text textInput, script string) *float64 {
	if len(text.words) < 8 || (script != "latin" && script != "cyrillic" && script != "greek") {
		return nil
	}
	letters := 0
	for _, word := range text.words {
		for _, r := range word {
			if unicode.IsLetter(r) {
				letters++
			}
		}
	}
	asl := float64(len(text.words)) / float64(len(text.sentences))
	awl := float64(letters) / float64(len(text.words))
	value := 1 - math.Max(0, asl-20)/30 - math.Max(0, awl-6)/4
	value = math.Max(0, math.Min(1, value))
	return &value
}

func rubricMedia(media []Media) (images, videos int, altCoverage *float64) {
	if len(media) == 0 {
		return 0, 0, nil
	}
	altCount := 0
	for _, item := range media {
		if item.Kind == "image" {
			images++
		}
		if item.Kind == "video" {
			videos++
		}
		if item.HasAlt {
			altCount++
		}
	}
	coverage := float64(altCount) / float64(len(media))
	return images, videos, &coverage
}

func rubricLinks(segments content.Segments) (active, hosts, bareURLs int) {
	active = len(segments.ActiveLinks)
	distinctHosts := make(map[string]struct{})
	for _, link := range segments.ActiveLinks {
		if parsed, err := content.ParseExternalURL(link); err == nil {
			distinctHosts[parsed.Host] = struct{}{}
		}
	}
	hosts = len(distinctHosts)
	for _, prose := range segments.Prose {
		bareURLs += len(rubricBareURLPattern.FindAllString(prose, -1))
	}
	return active, hosts, bareURLs
}

func rubricProseRunes(texts []string) int {
	total := 0
	for _, text := range texts {
		total += utf8.RuneCountInString(text)
	}
	return total
}

func rubricLength(chars int) Length {
	switch {
	case chars < 40:
		return LengthTiny
	case chars < 280:
		return LengthShort
	case chars < 1000:
		return LengthMedium
	default:
		return LengthLong
	}
}
