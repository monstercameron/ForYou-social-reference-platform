package server

import (
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"strconv"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/wire"
)

// demoSlots are the media slots every demo reply may name in its Markdown as
// ![label](fy-media:landscape-1). They stand for slots an author would declare
// and sign; each maps to a bundled picture.
var demoSlots, demoSlotPictures = func() ([]wire.MediaReference, map[string]int) {
	alt := demoImageAlt
	slots := make([]wire.MediaReference, 3)
	pictures := make(map[string]int, len(slots))
	for i := range slots {
		id := "landscape-" + strconv.Itoa(i+1)
		digest := sha256.Sum256([]byte(id))
		slots[i] = wire.MediaReference{ID: id, URL: "https://media.example/demo/" + id + ".png", Kind: "image", SHA256: hex.EncodeToString(digest[:]), AltText: &alt}
		pictures[id] = i + 1
	}
	return slots, pictures
}()

// replyMarkup renders reply text as CommonMark without extensions. External
// links stay inert, raw HTML is shown as text, and a demo slot named by an
// image becomes a picture listed in the second result.
func replyMarkup(text string) (template.HTML, []int) {
	rendered, err := content.Render(wire.PostPayload{ContentFormat: content.FormatMarkdown, Text: text, MediaSlots: &demoSlots}, content.RenderOptions{})
	if err != nil {
		return template.HTML(template.HTMLEscapeString(text)), nil
	}
	pictures := make([]int, 0, len(rendered.Media.Inline))
	for _, id := range rendered.Media.Inline {
		pictures = append(pictures, demoSlotPictures[id])
	}
	return rendered.HTML, pictures
}

// render fills in the reply's markup and the pictures it shows.
func (r *reply) render() {
	r.HTML, r.pictures = replyMarkup(r.Text)
	r.Images = make([]galleryPage, len(r.pictures))
	for i := range r.pictures {
		r.Images[i], _ = slideOf(r.PostID, r.Path, r.pictures, i+1)
	}
}

// Preview is the draft as it will look once posted.
func (d replyDraft) Preview() template.HTML {
	markup, _ := replyMarkup(d.Text)
	return markup
}

// PreviewPictures lists the bundled pictures the draft will show.
func (d replyDraft) PreviewPictures() []int {
	_, pictures := replyMarkup(d.Text)
	return pictures
}
