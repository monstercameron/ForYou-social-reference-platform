package content

import (
	"errors"
	"reflect"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func TestSegment(t *testing.T) {
	check := func(t *testing.T, payload wire.PostPayload, want Segments) {
		t.Helper()
		got, err := Segment(payload)
		if err != nil {
			t.Fatalf("Segment() error = %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Segment() = %#v; want %#v", got, want)
		}
	}

	t.Run("plain text", func(t *testing.T) {
		check(t, wire.PostPayload{ContentFormat: FormatPlain, Text: "Hello\n\nWorld\n  \nAgain\nmore"}, Segments{
			Prose:     []string{"Hello\n\nWorld\n  \nAgain\nmore"},
			Structure: Structure{Paragraphs: 3},
		})
	})

	t.Run("markdown mixed", func(t *testing.T) {
		check(t, wire.PostPayload{ContentFormat: FormatMarkdown, Text: "# Release notes\n\n- Faster *sync*\n- Fewer retries\n\nSee [the changelog](https://example.com/changelog), <https://docs.example.org/sync> and [old](http://old.example.net/). Use `make sync` &amp; relax.\n\n> Quoted <b>raw</b>\n\n```sh\nmake sync\n```"}, Segments{
			Prose:       []string{"Release notes", "Faster sync", "Fewer retries", "See the changelog,  and old. Use  & relax.", "Quoted <b>raw</b>"},
			CodeBlocks:  []string{"make sync\n"},
			CodeSpans:   []string{"make sync"},
			ActiveLinks: []string{"https://example.com/changelog", "https://docs.example.org/sync"},
			Structure:   Structure{Lists: 1, Headings: 1, Quotes: 1, Paragraphs: 2},
		})
	})

	t.Run("media image label", func(t *testing.T) {
		media := []wire.MediaReference{{ID: "fig1"}}
		check(t, wire.PostPayload{ContentFormat: FormatMarkdown, Text: "![diagram](fy-media:fig1)", MediaSlots: &media}, Segments{
			Prose:     []string{"diagram"},
			Structure: Structure{Paragraphs: 1},
		})
	})

	t.Run("soft break", func(t *testing.T) {
		check(t, wire.PostPayload{ContentFormat: FormatMarkdown, Text: "one\ntwo"}, Segments{
			Prose:     []string{"one\ntwo"},
			Structure: Structure{Paragraphs: 1},
		})
	})

	t.Run("inert link schemes", func(t *testing.T) {
		check(t, wire.PostPayload{ContentFormat: FormatMarkdown, Text: "<http://example.com/> and [x](javascript:alert(1))"}, Segments{
			Prose:     []string{"http://example.com/ and x"},
			Structure: Structure{Paragraphs: 1},
		})
	})

	t.Run("empty text with media", func(t *testing.T) {
		media := []wire.MediaReference{{ID: "fig1"}}
		check(t, wire.PostPayload{ContentFormat: FormatMarkdown, MediaSlots: &media}, Segments{})
	})
}

func TestSegmentUnsupportedFormat(t *testing.T) {
	t.Run("unsupported format", func(t *testing.T) {
		_, err := Segment(wire.PostPayload{ContentFormat: "text/html"})
		if !errors.Is(err, ErrUnsupportedFormat) {
			t.Fatalf("Segment() error = %v; want %v", err, ErrUnsupportedFormat)
		}
	})
}
