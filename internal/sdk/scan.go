package sdk

import (
	"regexp"
	"strconv"
	"strings"
)

// Finding describes a heuristic secret-like value found locally in a draft.
type Finding struct {
	// Field identifies the draft field containing the match.
	Field string
	// Kind identifies the heuristic pattern.
	Kind string
	// Excerpt masks the match except for its first four characters.
	Excerpt string
}

var pbSecretPatterns = []struct {
	kind    string
	pattern *regexp.Regexp
}{
	{"private_key_pem", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)},
	{"seed_or_private_hex", regexp.MustCompile(`(?i)(?:seed|private)[^\n]{0,24}[0-9a-f]{64}|[0-9a-f]{64}[^\n]{0,24}(?:seed|private)`)},
	{"service_key", regexp.MustCompile(`sk_(?:live|test)_[A-Za-z0-9]+`)},
	{"aws_access_key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"github_token", regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`)},
	{"slack_token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]+`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{"credential_url", regexp.MustCompile(`(?i)https://[^\s?#]+\?[^\s#]*(?:token|key|secret|signature)=[^&\s#]+`)},
}

// ScanDraft heuristically detects obvious credentials locally; findings are never transmitted.
func ScanDraft(d Draft) []Finding {
	fields := []struct{ name, value string }{{"text", d.Text}, {"content_format", d.ContentFormat}}
	for i, slot := range d.MediaSlots {
		prefix := "media_slots[" + strconv.Itoa(i) + "]."
		fields = append(fields, struct{ name, value string }{prefix + "id", slot.ID}, struct{ name, value string }{prefix + "url", slot.URL}, struct{ name, value string }{prefix + "kind", slot.Kind}, struct{ name, value string }{prefix + "sha256", slot.SHA256})
		if slot.AltText != nil {
			fields = append(fields, struct{ name, value string }{prefix + "alt_text", *slot.AltText})
		}
	}
	for i, ref := range d.Refs {
		prefix := "refs[" + strconv.Itoa(i) + "]."
		fields = append(fields, struct{ name, value string }{prefix + "id", ref.ID}, struct{ name, value string }{prefix + "target", ref.Target})
	}
	findings := make([]Finding, 0)
	for _, field := range fields {
		for _, candidate := range pbSecretPatterns {
			match := candidate.pattern.FindString(field.value)
			if match == "" {
				continue
			}
			findings = append(findings, Finding{Field: field.name, Kind: candidate.kind, Excerpt: pbMask(match)})
		}
	}
	return findings
}

func pbMask(value string) string {
	if len(value) <= 4 {
		return strings.Repeat("*", len(value))
	}
	return value[:4] + strings.Repeat("*", len(value)-4)
}
