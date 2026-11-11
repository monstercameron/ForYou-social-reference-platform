package rank

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"
)

func TestLangLexiconHashes(t *testing.T) {
	check := func(t *testing.T, filename, want string) {
		t.Helper()
		data, err := langLexicons.ReadFile("lexicon/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(data))
		if got != want {
			t.Errorf("list changed: bump RubricVersion: %s SHA-256 = %s; want %s", filename, got, want)
		}
	}
	t.Run("German stopwords", func(t *testing.T) {
		check(t, "stopwords-de.txt", "2ca0038c0ea1f553814ceefba70caacc8dd0e5dd1ea8c53341535d61176003b0")
	})
	t.Run("English stopwords", func(t *testing.T) {
		check(t, "stopwords-en.txt", "1399cad8ab65e40ba4acd40c89d21d280d924b6efb50fc12c0d4893e25b1d88a")
	})
	t.Run("Spanish stopwords", func(t *testing.T) {
		check(t, "stopwords-es.txt", "37ce62fee78de525da118d9fd069fb4d3be6a52165ddaad58a94cb0d48793da3")
	})
	t.Run("French stopwords", func(t *testing.T) {
		check(t, "stopwords-fr.txt", "319a030034801190ff08a25c0c918c69d8d50d3c15e19d659584ce75353d6cf8")
	})
	t.Run("Italian stopwords", func(t *testing.T) {
		check(t, "stopwords-it.txt", "a6adab1d3b90a482a7e98f8c675b2a4f1a4223bf985c506fbc65207af683c1d1")
	})
	t.Run("Dutch stopwords", func(t *testing.T) {
		check(t, "stopwords-nl.txt", "d4418683aceae8cca780f4e9a61da54dfb4123095fc58e240614af4c822dad10")
	})
	t.Run("Portuguese stopwords", func(t *testing.T) {
		check(t, "stopwords-pt.txt", "4e912d8feba216c8b7e673b9a3cbe9df912d51fee252892e93c72425470ea4df")
	})
	t.Run("topic lexicon", func(t *testing.T) {
		check(t, "topics.txt", "87709b1c8b6fbfe4c4b607f6c02459daac92a10bb159fb970e829647f40ef732")
	})
}

func TestLangLanguage(t *testing.T) {
	check := func(t *testing.T, words []string, want string) {
		t.Helper()
		if got := langLanguage(words); got != want {
			t.Errorf("langLanguage(%q) = %q; want %q", words, got, want)
		}
	}
	t.Run("English stopwords", func(t *testing.T) {
		check(t, []string{"the", "dog", "next", "door", "is", "in", "the", "garden", "and", "does", "not", "want", "to", "come", "out"}, "en")
	})
	t.Run("Spanish stopwords", func(t *testing.T) {
		check(t, []string{"el", "perro", "de", "mi", "vecino", "está", "en", "el", "jardín", "y", "no", "quiere", "salir"}, "es")
	})
	t.Run("French stopwords", func(t *testing.T) {
		check(t, []string{"le", "chat", "de", "ma", "voisine", "est", "sur", "le", "toit", "et", "il", "ne", "veut", "pas", "descendre"}, "fr")
	})
	t.Run("German stopwords", func(t *testing.T) {
		check(t, []string{"der", "hund", "von", "meinem", "nachbarn", "ist", "im", "garten", "und", "will", "nicht", "raus"}, "de")
	})
	t.Run("Portuguese stopwords", func(t *testing.T) {
		check(t, []string{"o", "cachorro", "do", "meu", "vizinho", "está", "no", "jardim", "e", "não", "quer", "sair"}, "pt")
	})
	t.Run("Italian stopwords", func(t *testing.T) {
		check(t, []string{"il", "cane", "del", "mio", "vicino", "è", "nel", "giardino", "e", "non", "vuole", "uscire"}, "it")
	})
	t.Run("Dutch stopwords", func(t *testing.T) {
		check(t, []string{"de", "hond", "van", "mijn", "buurman", "is", "in", "de", "tuin", "en", "wil", "niet", "naar", "buiten"}, "nl")
	})
	t.Run("five word boundary", func(t *testing.T) { check(t, []string{"free", "deals", "follow", "me", "now"}, "en") })
	t.Run("under five words", func(t *testing.T) { check(t, []string{"the", "dog", "is", "here"}, "und") })
	t.Run("unknown language", func(t *testing.T) {
		check(t, []string{"собака", "соседа", "сидит", "в", "саду", "и", "не", "хочет", "выходить"}, "und")
	})
	t.Run("y makes Spanish win outright", func(t *testing.T) { check(t, []string{"de", "x", "y", "z", "w"}, "es") })
	t.Run("alphabetical tie", func(t *testing.T) { check(t, []string{"de", "x", "q", "z", "w"}, "es") })
	t.Run("rate below threshold", func(t *testing.T) {
		check(t, []string{"maintainers", "reviewing", "replication", "changes", "should", "compare", "acknowledgement", "latency"}, "und")
	})
}

func TestLangScript(t *testing.T) {
	check := func(t *testing.T, prose []string, want string) {
		t.Helper()
		if got := langScript(prose); got != want {
			t.Errorf("langScript(%q) = %q; want %q", prose, got, want)
		}
	}
	t.Run("Cyrillic prose", func(t *testing.T) {
		check(t, []string{"Собака соседа сидит в саду и не хочет выходить."}, "cyrillic")
	})
	t.Run("Greek prose", func(t *testing.T) {
		check(t, []string{"Ο σκύλος του γείτονα είναι στον κήπο."}, "greek")
	})
	t.Run("Hiragana beats Han", func(t *testing.T) { check(t, []string{"隣の犬は庭にいます。"}, "hiragana") })
	t.Run("Cyrillic beats Latin", func(t *testing.T) { check(t, []string{"Привет hello"}, "cyrillic") })
	t.Run("Latin prose", func(t *testing.T) { check(t, []string{"The API uses TLS"}, "latin") })
	t.Run("no letters", func(t *testing.T) { check(t, []string{"🔥 123"}, "") })
}

func TestLangTopics(t *testing.T) {
	check := func(t *testing.T, words []string, hasCodeBlock bool, want []string) {
		t.Helper()
		if got := langTopics(words, hasCodeBlock); !reflect.DeepEqual(got, want) {
			t.Errorf("langTopics(%q, %t) = %v; want %v", words, hasCodeBlock, got, want)
		}
	}
	t.Run("tomato post", func(t *testing.T) {
		check(t, []string{"tomatoes", "ripened", "on", "the", "windowsill", "this", "week", "three", "of", "them", "finally"}, false, []string{"garden"})
	})
	t.Run("code answer", func(t *testing.T) {
		check(t, []string{"use", "instead", "of", "comparing", "error", "strings", "the", "wrapped", "error", "keeps", "its", "cause"}, true, []string{"tech"})
	})
	t.Run("plural art term", func(t *testing.T) { check(t, []string{"photos"}, false, []string{"art"}) })
	t.Run("sports ref suffix case", func(t *testing.T) { check(t, []string{"ref", "goal"}, false, []string{"sports"}) })
	t.Run("three topic cap with deterministic ties", func(t *testing.T) {
		check(t, []string{"dog", "cat", "band", "song", "concert", "bread", "soup", "dinner", "flight", "hotel"}, false, []string{"animals", "food", "music"})
	})
	t.Run("single hit in long post", func(t *testing.T) {
		check(t, []string{"the", "dog", "sat", "by", "the", "door", "and", "we", "all", "waited", "for", "the", "rain", "to", "stop", "before", "going", "out", "into", "the", "wet", "grey", "street", "with", "our", "coats", "on"}, false, nil)
	})
	t.Run("no topic hits", func(t *testing.T) { check(t, []string{"free", "deals", "follow", "me", "now"}, false, nil) })
	t.Run("code block gives tech one hit for short text", func(t *testing.T) { check(t, []string{"ordinary", "prose"}, true, []string{"tech"}) })
}
