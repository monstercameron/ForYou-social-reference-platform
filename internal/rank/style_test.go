package rank

import (
	"math"
	"testing"
)

func TestStyleShouting(t *testing.T) {
	check := func(t *testing.T, prose []string, wantRatio float64, wantRuns int) {
		t.Helper()
		gotRatio, gotRuns := styleShouting(prose)
		if math.Abs(gotRatio-wantRatio) > 1e-6 || gotRuns != wantRuns {
			t.Errorf("styleShouting(%q) = (%f, %d); want (%f, %d)", prose, gotRatio, gotRuns, wantRatio, wantRuns)
		}
	}
	t.Run("marketing caps with short bang runs", func(t *testing.T) { check(t, []string{"FREE DEALS!! FOLLOW ME NOW!!"}, 1, 0) })
	t.Run("shouted insult", func(t *testing.T) { check(t, []string{"WHAT A SHIT TAKE. ABSOLUTE GARBAGE."}, 1, 0) })
	t.Run("fewer than twenty cased letters", func(t *testing.T) { check(t, []string{"OK FINE."}, 0, 0) })
	t.Run("acronyms", func(t *testing.T) { check(t, []string{"The API uses TLS, JSON and HTTP over TCP."}, 0.580645, 0) })
	t.Run("mixed punctuation runs", func(t *testing.T) { check(t, []string{"What?!?! No way!!! Really???"}, 0, 3) })
}

func TestStyleRepetition(t *testing.T) {
	check := func(t *testing.T, prose, words []string, want Repetition) {
		t.Helper()
		got := styleRepetition(prose, words)
		if math.Abs(got.Rep3-want.Rep3) > 1e-6 || math.Abs(got.CharRepeat-want.CharRepeat) > 1e-6 || got.Emoji != want.Emoji || math.Abs(got.EmojiRatio-want.EmojiRatio) > 1e-6 {
			t.Errorf("styleRepetition(%q, %q) = %+v; want %+v", prose, words, got, want)
		}
	}
	t.Run("repeated words and phrase", func(t *testing.T) {
		check(t, []string{"buy now buy now buy now buy now buy now"}, []string{"buy", "now", "buy", "now", "buy", "now", "buy", "now", "buy", "now"}, Repetition{Rep3: 0.75, CharRepeat: 1})
	})
	t.Run("single repeated character", func(t *testing.T) {
		check(t, []string{"hahahahahahahaha"}, []string{"hahahahahahahaha"}, Repetition{CharRepeat: 1})
	})
	t.Run("period three below minimum", func(t *testing.T) { check(t, []string{"abcabcabc"}, nil, Repetition{}) })
	t.Run("period three at minimum", func(t *testing.T) { check(t, []string{"abcabcabcabc"}, nil, Repetition{CharRepeat: 1}) })
	t.Run("whitespace only stretch", func(t *testing.T) { check(t, []string{"a          b"}, []string{"a", "b"}, Repetition{}) })
	t.Run("fewer than six trigrams", func(t *testing.T) {
		check(t, []string{"FREE DEALS FOLLOW ME NOW"}, []string{"free", "deals", "follow", "me", "now"}, Repetition{})
	})
	t.Run("emoji scalars", func(t *testing.T) {
		check(t, []string{"🔥🔥🔥🚀🚀🎉 launch day"}, nil, Repetition{Emoji: 6, EmojiRatio: 0.4})
	})
	t.Run("skin tone modifier excluded", func(t *testing.T) { check(t, []string{"👍🏽 ok"}, nil, Repetition{Emoji: 1, EmojiRatio: 0.25}) })
	t.Run("regional indicators excluded", func(t *testing.T) { check(t, []string{"🇩🇪"}, nil, Repetition{}) })
}

func TestStyleEmoji(t *testing.T) {
	check := func(t *testing.T, value rune, want bool) {
		t.Helper()
		if got := styleEmoji(value); got != want {
			t.Errorf("styleEmoji(%U) = %t; want %t", value, got, want)
		}
	}
	t.Run("copyright sign", func(t *testing.T) { check(t, '\u00a9', true) })
	t.Run("skin tone modifier", func(t *testing.T) { check(t, '\U0001f3fb', false) })
	t.Run("grinning face", func(t *testing.T) { check(t, '\U0001f600', true) })
	t.Run("ordinary letter", func(t *testing.T) { check(t, 'A', false) })
	t.Run("number sign", func(t *testing.T) { check(t, '#', false) })
	t.Run("digit", func(t *testing.T) { check(t, '1', false) })
	t.Run("asterisk", func(t *testing.T) { check(t, '*', false) })
	t.Run("variation selector", func(t *testing.T) { check(t, '\ufe0f', false) })
	t.Run("joiner", func(t *testing.T) { check(t, '\u200d', false) })
	t.Run("regional indicator", func(t *testing.T) { check(t, '\U0001f1e6', false) })
	t.Run("rocket", func(t *testing.T) { check(t, '\U0001f680', true) })
	t.Run("heart", func(t *testing.T) { check(t, '\u2764', true) })
}
