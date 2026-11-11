package rank

import (
	"reflect"
	"strings"
	"testing"
)

func TestTextAnalyze(t *testing.T) {
	check := func(t *testing.T, prose []string, sentences, words int, first []string, contains string) {
		t.Helper()
		got := textAnalyze(prose)
		if len(got.sentences) != sentences || len(got.words) != words {
			t.Fatalf("textAnalyze() = %d sentences, %d words; want %d, %d", len(got.sentences), len(got.words), sentences, words)
		}
		if first != nil && !reflect.DeepEqual(got.sentences[0], first) {
			t.Errorf("first sentence = %#v; want %#v", got.sentences[0], first)
		}
		if contains != "" && !strings.Contains(" "+strings.Join(got.words, " ")+" ", " "+contains+" ") {
			t.Errorf("words %#v do not contain %q", got.words, contains)
		}
	}
	t.Run("sentences and words", func(t *testing.T) {
		check(t, []string{"Shit. Revoked, thanks. Posts here can't be edited, so that one stays up for good."}, 3, 15, []string{"shit"}, "can't")
	})
	t.Run("curly apostrophe", func(t *testing.T) {
		got := textAnalyze([]string{"‘quoted’ words and you’re fine"})
		want := []string{"quoted", "words", "and", "you're", "fine"}
		if !reflect.DeepEqual(got.words, want) {
			t.Fatalf("words = %#v; want %#v", got.words, want)
		}
	})
	t.Run("no split inside tokens", func(t *testing.T) {
		got := textAnalyze([]string{"Mirror at http://old.example.net/notes."})
		want := []string{"mirror", "at", "http", "old", "example", "net", "notes"}
		if len(got.sentences) != 1 || !reflect.DeepEqual(got.words, want) {
			t.Fatalf("got %#v; want one sentence and %#v", got, want)
		}
	})
	t.Run("star-only run is no word", func(t *testing.T) {
		got := textAnalyze([]string{"****"})
		if len(got.sentences) != 0 || len(got.words) != 0 {
			t.Fatalf("got %#v; want empty", got)
		}
	})
	t.Run("terminator runs and unicode", func(t *testing.T) {
		got := textAnalyze([]string{"One？！ 2.5 stays. 東京。次"})
		if len(got.sentences) != 3 || !strings.Contains(" "+strings.Join(got.words, " ")+" ", " 東京 ") {
			t.Fatalf("got %#v; want three sentences", got)
		}
	})
}

func TestTextNorm(t *testing.T) {
	check := func(t *testing.T, input, want string) {
		t.Helper()
		if got := textNorm(input); got != want {
			t.Errorf("textNorm(%q) = %q; want %q", input, got, want)
		}
	}
	t.Run("leet and repeated characters", func(t *testing.T) { check(t, "sh1t", "shit"); check(t, "$h00ttt", "shoott") })
	t.Run("wildcard remains", func(t *testing.T) { check(t, "f*ck1ng", "f*cking") })
}

func TestTextLoadLexicon(t *testing.T) {
	t.Run("comment-only lexicon", func(t *testing.T) {
		if got := textLoadLexicon([]byte("# owner managed\n")); len(got) != 0 {
			t.Fatalf("parsed entries = %#v; want empty", got)
		}
	})
	t.Run("repeated letters rejected", func(t *testing.T) {
		textTestLexiconPanics(t, []byte("faaa\n"))
	})
	t.Run("missing final newline rejected", func(t *testing.T) {
		textTestLexiconPanics(t, []byte("entry"))
	})
	t.Run("CRLF rejected", func(t *testing.T) {
		textTestLexiconPanics(t, []byte("entry\r\n"))
	})
}

func textTestLexiconPanics(t *testing.T, data []byte) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("textLoadLexicon did not panic")
		}
	}()
	textLoadLexicon(data)
}
