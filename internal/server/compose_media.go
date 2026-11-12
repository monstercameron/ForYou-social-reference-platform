package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

const (
	cmpMaxMedia = 16
	cmpMaxRefs  = 32
	cmpMaxAlt   = 2000
)

var (
	cmpSlotID = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	cmpDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	cmpInline = regexp.MustCompile(`\]\(fy-(media|ref):([a-z0-9_-]{1,32})\)`)
)

// cmpMediaPolicy decides which addresses a media fetch may reach. Only global addresses are allowed; tests replace the resolver.
var cmpMediaPolicy = content.DestinationPolicy{}

// cmpMediaRow is one external media slot as the author typed it. Origin is the host the link fetches from, and Notice says why a fetch to compute the digest failed.
type cmpMediaRow struct {
	Index                              int
	Slot, Kind, URL, SHA256, Size, Alt string
	Origin, Notice                     string
}

// cmpRefRow is one signed structured reference as the author typed it.
type cmpRefRow struct {
	Index      int
	ID, Target string
}

func (r cmpMediaRow) blank() bool {
	return r.Slot == "" && r.URL == "" && r.SHA256 == "" && r.Size == "" && r.Alt == ""
}

func (r cmpRefRow) blank() bool {
	return r.ID == "" && r.Target == ""
}

func cmpReadMedia(values url.Values) []cmpMediaRow {
	slots, kinds, urls := values["media_slot"], values["media_kind"], values["media_url"]
	digests, sizes, alts := values["media_sha256"], values["media_size"], values["media_alt"]
	rows := make([]cmpMediaRow, cmpLongest(slots, kinds, urls, digests, sizes, alts))
	for i := range rows {
		rows[i] = cmpMediaRow{
			Index:  i,
			Slot:   cmpAt(slots, i),
			Kind:   cmpAt(kinds, i),
			URL:    cmpAt(urls, i),
			SHA256: cmpAt(digests, i),
			Size:   cmpAt(sizes, i),
			Alt:    cmpAt(alts, i),
			Origin: cmpOriginOf(cmpAt(urls, i)),
		}
	}
	return rows
}

func cmpReadRefs(values url.Values) []cmpRefRow {
	ids, targets := values["ref_id"], values["ref_target"]
	rows := make([]cmpRefRow, cmpLongest(ids, targets))
	for i := range rows {
		rows[i] = cmpRefRow{Index: i, ID: cmpAt(ids, i), Target: cmpAt(targets, i)}
	}
	return rows
}

// cmpOriginOf names the HTTPS origin a media link fetches from, or nothing when the link is not HTTPS.
func cmpOriginOf(link string) string {
	parsed, err := url.Parse(link)
	if err != nil {
		return ""
	}
	origin, err := content.Origin(parsed)
	if err != nil {
		return ""
	}
	return origin
}

// cmpMediaRows turns the media of a stored post back into form rows, so that a new version starts from them.
func cmpMediaRows(slots *[]wire.MediaReference) []cmpMediaRow {
	if slots == nil {
		return nil
	}
	rows := make([]cmpMediaRow, 0, len(*slots))
	for i, slot := range *slots {
		row := cmpMediaRow{Index: i, Slot: slot.ID, Kind: slot.Kind, URL: slot.URL, SHA256: slot.SHA256, Origin: cmpOriginOf(slot.URL)}
		if slot.SizeBytes > 0 {
			row.Size = strconv.FormatInt(slot.SizeBytes, 10)
		}
		if slot.AltText != nil {
			row.Alt = *slot.AltText
		}
		rows = append(rows, row)
	}
	return rows
}

// cmpRefRows turns the references of a stored post back into form rows.
func cmpRefRows(refs *[]wire.PostReference) []cmpRefRow {
	if refs == nil {
		return nil
	}
	rows := make([]cmpRefRow, 0, len(*refs))
	for i, ref := range *refs {
		rows = append(rows, cmpRefRow{Index: i, ID: ref.ID, Target: ref.Target})
	}
	return rows
}

func cmpLongest(columns ...[]string) int {
	longest := 0
	for _, column := range columns {
		longest = max(longest, len(column))
	}
	return longest
}

func cmpAt(values []string, i int) string {
	if i < len(values) {
		return values[i]
	}
	return ""
}

// cmpMediaSlots checks the media rows and returns the slots of a draft. Media is only linked; nothing is fetched here.
func cmpMediaSlots(rows []cmpMediaRow) ([]wire.MediaReference, []string) {
	var slots []wire.MediaReference
	var problems []string
	for _, row := range rows {
		if row.blank() {
			continue
		}
		if !cmpSlotID.MatchString(row.Slot) {
			problems = append(problems, "Each media slot needs an ID of lowercase letters, digits, - or _, up to 32 characters.")
		}
		if row.Kind != "image" && row.Kind != "video" {
			problems = append(problems, "Media kind must be image or video.")
		}
		if !strings.HasPrefix(row.URL, "https://") {
			problems = append(problems, "Media links must use HTTPS.")
		}
		if !cmpDigest.MatchString(row.SHA256) {
			problems = append(problems, "The SHA-256 must be 64 lowercase hex characters.")
		}
		slot := wire.MediaReference{ID: row.Slot, URL: row.URL, Kind: row.Kind, SHA256: row.SHA256}
		if row.Size != "" {
			size, err := strconv.ParseInt(row.Size, 10, 64)
			if err != nil || size < 0 {
				problems = append(problems, "The size must be a whole number of bytes.")
			} else {
				slot.SizeBytes = size
			}
		}
		if row.Alt != "" {
			if utf8.RuneCountInString(row.Alt) > cmpMaxAlt {
				problems = append(problems, "Alt text can be at most 2,000 characters.")
			}
			alt := row.Alt
			slot.AltText = &alt
		}
		slots = append(slots, slot)
	}
	if len(slots) > cmpMaxMedia {
		problems = append(problems, "A post has at most 16 media slots.")
	}
	return slots, problems
}

// cmpRefs checks the reference rows and returns the references of a draft.
func cmpRefs(rows []cmpRefRow) ([]wire.PostReference, []string) {
	var refs []wire.PostReference
	var problems []string
	for _, row := range rows {
		if row.blank() {
			continue
		}
		if !cmpSlotID.MatchString(row.ID) {
			problems = append(problems, "Each reference needs an ID of lowercase letters, digits, - or _, up to 32 characters.")
		}
		if strings.TrimSpace(row.Target) == "" {
			problems = append(problems, "Each reference needs a target: a message ID or a did:key.")
		}
		refs = append(refs, wire.PostReference{ID: row.ID, Target: row.Target})
	}
	if len(refs) > cmpMaxRefs {
		problems = append(problems, "A post has at most 32 references.")
	}
	return refs, problems
}

// cmpEditRows applies one add or remove action from the form, then renumbers the rows.
func cmpEditRows(form cmpForm, action string) cmpForm {
	verb, rest, _ := strings.Cut(action, ":")
	index, err := strconv.Atoi(rest)
	switch verb {
	case "add-media":
		if len(form.Media) < cmpMaxMedia {
			form.Media = append(form.Media, cmpMediaRow{Kind: "image"})
		}
	case "remove-media":
		if err == nil && index >= 0 && index < len(form.Media) {
			form.Media = append(form.Media[:index], form.Media[index+1:]...)
		}
	case "add-ref":
		if len(form.Refs) < cmpMaxRefs {
			form.Refs = append(form.Refs, cmpRefRow{})
		}
	case "remove-ref":
		if err == nil && index >= 0 && index < len(form.Refs) {
			form.Refs = append(form.Refs[:index], form.Refs[index+1:]...)
		}
	}
	for i := range form.Media {
		form.Media[i].Index = i
	}
	for i := range form.Refs {
		form.Refs[i].Index = i
	}
	return form
}

// cmpDigestRow fetches the media of one row to compute its SHA-256 and size. A failed fetch keeps the row and says why.
func cmpDigestRow(ctx context.Context, form cmpForm, index string) cmpForm {
	i, err := strconv.Atoi(index)
	if err != nil || i < 0 || i >= len(form.Media) {
		return form
	}
	row := &form.Media[i]
	digest, size, problem := cmpFetchMedia(ctx, row.URL, row.Kind)
	if problem != "" {
		row.Notice = problem
		return form
	}
	row.SHA256 = digest
	row.Size = strconv.FormatInt(size, 10)
	row.Notice = ""
	return form
}

// cmpFetchMedia downloads a media link over HTTPS and returns the SHA-256 and size of the exact file. The problem it returns is for the author.
func cmpFetchMedia(ctx context.Context, link, kind string) (string, int64, string) {
	request, err := content.NewFetchRequest(ctx, link)
	if err != nil {
		return "", 0, "The link must be a valid HTTPS URL."
	}
	client := content.NewFetchClient(content.FetchConfig{Policy: cmpMediaPolicy})
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, content.ErrBlockedDestination) {
			return "", 0, "Blocked: private or loopback address"
		}
		return "", 0, "The media could not be fetched."
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", 0, "The media could not be fetched."
	}
	data, err := content.ReadLimited(response.Body, content.DefaultMediaBudget().MaxBytes)
	if errors.Is(err, content.ErrResponseTooLarge) {
		return "", 0, "Too large"
	}
	if err != nil {
		return "", 0, "The media could not be fetched."
	}
	media, err := content.SniffMediaType(data)
	if err != nil || !cmpKindMatches(kind, media) {
		return "", 0, "Unsupported type"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), ""
}

// cmpKindMatches reports whether the sniffed bytes are a type of the declared media kind.
func cmpKindMatches(kind string, media content.MediaType) bool {
	switch kind {
	case "image":
		return media == content.MediaJPEG || media == content.MediaPNG || media == content.MediaGIF || media == content.MediaWebP
	case "video":
		return media == content.MediaMP4 || media == content.MediaWebM
	}
	return false
}

// cmpInert names the fy-media and fy-ref links in Markdown text that no slot or reference declares. Readers see them as plain text.
func cmpInert(draft sdk.Draft) []string {
	if draft.ContentFormat != content.FormatMarkdown {
		return nil
	}
	declared := make(map[string]bool, len(draft.MediaSlots)+len(draft.Refs))
	for _, slot := range draft.MediaSlots {
		declared["media:"+slot.ID] = true
	}
	for _, ref := range draft.Refs {
		declared["ref:"+ref.ID] = true
	}
	var lines []string
	for _, match := range cmpInline.FindAllStringSubmatch(draft.Text, -1) {
		if declared[match[1]+":"+match[2]] {
			continue
		}
		if match[1] == "media" {
			lines = append(lines, "fy-media:"+match[2]+" has no media slot, so it stays inert text.")
		} else {
			lines = append(lines, "fy-ref:"+match[2]+" has no declared reference, so it stays inert text.")
		}
	}
	return lines
}
