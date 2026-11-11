package rank

import (
	_ "embed"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

//go:embed lexicon/bip39-english.txt
var safetyBIP39Data string

// SecretMatch is one credential-shaped string found in a post (rubric R8).
type SecretMatch struct {
	Kind string // Kind is the stable credential pattern identifier.
	Text string // Text is the source text matched by the pattern.
}

type safetySecretPattern struct {
	kind    string
	pattern *regexp.Regexp
}

var safetySecretPatterns = []safetySecretPattern{
	{kind: "pem_private_key", pattern: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{kind: "aws_access_key_id", pattern: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{kind: "github_token", pattern: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36}\b|\bgithub_pat_[A-Za-z0-9_]{82}\b`)},
	{kind: "slack_token", pattern: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{kind: "stripe_live_key", pattern: regexp.MustCompile(`\b[sr]k_live_[A-Za-z0-9]{24,}`)},
	{kind: "google_api_key", pattern: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{kind: "jwt", pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{kind: "assigned_secret", pattern: regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?key|secret|token|passw(?:or)?d|private[_-]?key)\b["']?\s*[:=]\s*["']?([A-Za-z0-9_+/=-]{16,})`)},
	{kind: "hex_private_key", pattern: regexp.MustCompile(`\b(?:0x)?[0-9a-fA-F]{64}\b`)},
}

var safetyHexCuePattern = regexp.MustCompile(`(?i)(private key|privkey|secret)`)

var safetyBIP39 = safetyLoadBIP39()

func safetyLoadBIP39() map[string]bool {
	words := make(map[string]bool, 2048)
	for _, word := range strings.Split(strings.TrimSuffix(safetyBIP39Data, "\n"), "\n") {
		words[word] = true
	}
	if len(words) != 2048 {
		panic("embedded BIP-39 English list must contain 2048 words")
	}
	return words
}

func safetySecrets(texts []string) []SecretMatch {
	unique := make(map[SecretMatch]bool)
	for _, source := range texts {
		for _, candidate := range safetySecretPatterns {
			for _, bounds := range candidate.pattern.FindAllStringSubmatchIndex(source, -1) {
				start, end := bounds[0], bounds[1]
				value := source[start:end]
				if candidate.kind == "assigned_secret" {
					value = source[bounds[4]:bounds[5]]
					if safetyEntropy(value) < 3.5 {
						continue
					}
				}
				if candidate.kind == "hex_private_key" && !safetyHasKeyCue(source, start) {
					continue
				}
				unique[SecretMatch{Kind: candidate.kind, Text: value}] = true
			}
		}
		safetyAddRecoveryMatches(source, safetyBIP39, unique)
	}
	if len(unique) == 0 {
		return nil
	}
	matches := make([]SecretMatch, 0, len(unique))
	for match := range unique {
		matches = append(matches, match)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Kind == matches[j].Kind {
			return matches[i].Text < matches[j].Text
		}
		return matches[i].Kind < matches[j].Kind
	})
	return matches
}

func safetyEntropy(value string) float64 {
	counts := make(map[rune]int)
	length := 0
	for _, r := range value {
		counts[r]++
		length++
	}
	entropy := 0.0
	for _, count := range counts {
		probability := float64(count) / float64(length)
		entropy -= probability * math.Log2(probability)
	}
	return entropy
}

func safetyHasKeyCue(source string, start int) bool {
	from := start - 40
	if from < 0 {
		from = 0
	}
	return safetyHexCuePattern.MatchString(source[from:start])
}

func safetyAddRecoveryMatches(source string, bip39 map[string]bool, matches map[SecretMatch]bool) {
	type safetyToken struct {
		start int
		end   int
	}
	var run []safetyToken
	safetyFlush := func() {
		if safetyIsRecoverySize(len(run)) {
			matches[SecretMatch{Kind: "recovery_phrase", Text: source[run[0].start:run[len(run)-1].end]}] = true
		}
		run = nil
	}
	for start := 0; start < len(source); {
		for start < len(source) {
			r, size := rune(source[start]), 1
			if r >= 0x80 {
				r, size = utf8.DecodeRuneInString(source[start:])
			}
			if !unicode.IsSpace(r) {
				break
			}
			start += size
		}
		if start == len(source) {
			break
		}
		end := start
		for end < len(source) {
			r, size := rune(source[end]), 1
			if r >= 0x80 {
				r, size = utf8.DecodeRuneInString(source[end:])
			}
			if unicode.IsSpace(r) {
				break
			}
			end += size
		}
		token := source[start:end]
		valid := token != "" && bip39[token]
		for _, r := range token {
			if r < 'a' || r > 'z' {
				valid = false
			}
		}
		if valid {
			run = append(run, safetyToken{start: start, end: end})
		} else {
			safetyFlush()
		}
		start = end
	}
	safetyFlush()
}

func safetyIsRecoverySize(size int) bool {
	return size == 12 || size == 15 || size == 18 || size == 21 || size == 24
}
