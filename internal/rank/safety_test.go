package rank

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"
	"testing"
)

func TestSafetyLexiconHashes(t *testing.T) {
	check := func(t *testing.T, file, want string) {
		t.Helper()
		data, err := os.ReadFile("lexicon/" + file)
		if err != nil {
			t.Fatal(err)
		}
		got := sha256.Sum256(data)
		if hex.EncodeToString(got[:]) != want {
			t.Errorf("%s hash = %s; want %s (bump RubricVersion for intentional list changes)", file, hex.EncodeToString(got[:]), want)
		}
	}
	t.Run("profanity mild", func(t *testing.T) {
		check(t, "profanity-mild.txt", "375c95de27d9fa078a6b5c5c685e9231eda0aff464534a684320cd67c5548e20")
	})
	t.Run("profanity strong", func(t *testing.T) {
		check(t, "profanity-strong.txt", "4b8b9ee4f95a765e9ceb13bb3f6a645d93e3feafc7bbf2c6e924d4f85597be7b")
	})
	t.Run("threat intent", func(t *testing.T) {
		check(t, "threat-intent.txt", "c057f88edd5e3dcd197b5a739cbabdf1caa27210237c828b580fb099a9c6821c")
	})
	t.Run("threat verbs", func(t *testing.T) {
		check(t, "threat-verbs.txt", "fd2f1165b0e97dfe3b6b127ab0838da367485d468ff112c3d0562b3c3e6632c7")
	})
	t.Run("threat targets", func(t *testing.T) {
		check(t, "threat-targets.txt", "6c67b7c6a3c8e1fec539c54cf371ed631ddd5092b11cda6f7db93509ccf1974d")
	})
	t.Run("harass insults", func(t *testing.T) {
		check(t, "harass-insults.txt", "544721e26485db8a926be9670bdea3eec8125a0ce8cdb32ec7799aa7af79906c")
	})
	t.Run("harass exclusion", func(t *testing.T) {
		check(t, "harass-exclusion.txt", "3022c5c9aa01d36fc558183a17fb079c09eb8fff88a4e0852e8faf033a048aa3")
	})
	t.Run("promotion", func(t *testing.T) {
		check(t, "promo.txt", "c8249f70a7555f344ef98eca82a8e29ed9d2fb47d56f77900de0b93c42b51965")
	})
	t.Run("solicitation objects", func(t *testing.T) {
		check(t, "solicit-objects.txt", "813dbea653201a0b7e22fb8cde5394ef533a60daa2be505586040d7d134e703f")
	})
	t.Run("adult labels", func(t *testing.T) {
		check(t, "adult.txt", "8d9227b986f469c503e53e62b8074a1a85bf5d305e5169a9ebc1bb888eecdad6")
	})
}

func TestSafetyHateLexicons(t *testing.T) {
	checkEntry := func(t *testing.T, lexicon textLexicon, term string) {
		t.Helper()
		if !slices.ContainsFunc(lexicon, func(entry []string) bool { return slices.Equal(entry, []string{term}) }) {
			t.Errorf("lexicon lacks %q", term)
		}
	}
	t.Run("identity and dehumanizing entries", func(t *testing.T) {
		identity, err := os.ReadFile("lexicon/hate-identity.txt")
		if err != nil {
			t.Fatal(err)
		}
		dehumanizing, err := os.ReadFile("lexicon/hate-dehumanizing.txt")
		if err != nil {
			t.Fatal(err)
		}
		identityLexicon := textLoadLexicon(identity)
		dehumanizingLexicon := textLoadLexicon(dehumanizing)
		if len(identityLexicon) == 0 || len(dehumanizingLexicon) == 0 {
			t.Fatal("hate lexicons must not be empty")
		}
		t.Run("immigrants", func(t *testing.T) { checkEntry(t, identityLexicon, "immigrants") })
		t.Run("refugees", func(t *testing.T) { checkEntry(t, identityLexicon, "refugees") })
		t.Run("muslims", func(t *testing.T) { checkEntry(t, identityLexicon, "muslims") })
		t.Run("jews", func(t *testing.T) { checkEntry(t, identityLexicon, "jews") })
		t.Run("christians", func(t *testing.T) { checkEntry(t, identityLexicon, "christians") })
		t.Run("women", func(t *testing.T) { checkEntry(t, identityLexicon, "women") })
		t.Run("gays", func(t *testing.T) { checkEntry(t, identityLexicon, "gays") })
		t.Run("vermin", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "vermin") })
		t.Run("animals", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "animals") })
		t.Run("parasites", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "parasites") })
		t.Run("disease", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "disease") })
		t.Run("trash", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "trash") })
		t.Run("cockroaches", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "cockroaches") })
		t.Run("rats", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "rats") })
		t.Run("plague", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "plague") })
		t.Run("subhuman", func(t *testing.T) { checkEntry(t, dehumanizingLexicon, "subhuman") })
	})
	t.Run("owner managed slur list parses", func(t *testing.T) {
		data, err := os.ReadFile("lexicon/hate-slurs.txt")
		if err != nil {
			t.Fatal(err)
		}
		_ = textLoadLexicon(data)
	})
}

func TestSafetyProfanity(t *testing.T) {
	check := func(t *testing.T, input string, mild, strong int) {
		t.Helper()
		gotMild, gotStrong := safetyProfanity(textAnalyze([]string{input}))
		if gotMild != mild || gotStrong != strong {
			t.Errorf("safetyProfanity() = (%d, %d); want (%d, %d)", gotMild, gotStrong, mild, strong)
		}
	}
	t.Run("fixture", func(t *testing.T) {
		check(t, "Shit. Revoked, thanks. Posts here can't be edited, so that one stays up for good.", 0, 1)
	})
	t.Run("strong insult", func(t *testing.T) {
		check(t, "Garbage tool. Wasted my whole fucking morning on false positives.", 0, 1)
	})
	t.Run("mild profanity", func(t *testing.T) { check(t, "It looks crooked as hell in my head and I haven't even seen it.", 1, 0) })
	t.Run("damn", func(t *testing.T) { check(t, "Don't spoil it, damn it. Some of us are still stuck.", 1, 0) })
	t.Run("masked words", func(t *testing.T) { check(t, "What a f*ck1ng mess, sh1t.", 0, 2) })
	t.Run("masked long word", func(t *testing.T) { check(t, "What a f***ing mess", 0, 1) })
	t.Run("whole word near miss", func(t *testing.T) { check(t, "Scunthorpe United won at home.", 0, 0) })
}

func TestSafetyThreat(t *testing.T) {
	check := func(t *testing.T, input string, want float64) {
		t.Helper()
		if got := safetyThreat(textAnalyze([]string{input})); got != want {
			t.Errorf("safetyThreat() = %v; want %v", got, want)
		}
	}
	t.Run("full design example", func(t *testing.T) { check(t, "Keep talking and I will find you and hurt you.", 1) })
	t.Run("fixture full threat", func(t *testing.T) {
		check(t, "I'm going to burn your house down with you in it. I have your address.", 1)
	})
	t.Run("fixture contracted intent", func(t *testing.T) {
		check(t, "The ref should watch his back in the car park. I'm serious, I'll smash his face in.", 1)
	})
	t.Run("verb and target only", func(t *testing.T) { check(t, "Kick his head in. Wish I'd been there to help.", 0.5) })
	t.Run("game near miss", func(t *testing.T) { check(t, "I'm going to kill it at the gig tonight", 0) })
	t.Run("accepted game false positive", func(t *testing.T) { check(t, "I'm going to kill you next round", 1) })
	t.Run("unlisted break verb", func(t *testing.T) { check(t, "I'll find you too and break your fucking legs.", 0) })
	t.Run("unlisted bat threat", func(t *testing.T) {
		check(t, "I know which building you work in. One more post like this and I'll be waiting outside with a bat.", 0)
	})
	t.Run("unlisted bullet threat", func(t *testing.T) { check(t, "Someone should put a bullet in people like this.", 0) })
	t.Run("counter speech", func(t *testing.T) {
		check(t, "If you're this angry, please talk to someone before you do anything. I mean that kindly.", 0)
	})
}

func TestSafetyHate(t *testing.T) {
	check := func(t *testing.T, input string, want Hate) {
		t.Helper()
		if got := safetyHate(textAnalyze([]string{input})); got != want {
			t.Errorf("safetyHate() = %#v; want %#v", got, want)
		}
	}
	neutral := Hate{}
	t.Run("identity dehumanization", func(t *testing.T) {
		check(t, "Immigrants are vermin and should be driven out of this town.", Hate{Dehumanizing: true})
	})
	t.Run("unrelated vermin", func(t *testing.T) {
		check(t, "Refugees are welcome here, and the vermin in the walls are the landlord's problem.", neutral)
	})
	t.Run("pronoun is not identity", func(t *testing.T) {
		check(t, "They're animals, not neighbors. They don't belong in this country.", neutral)
	})
	t.Run("send them back is not identity match", func(t *testing.T) {
		check(t, "Late hours just mean more of those immigrants hanging around. Send them back where they came from.", neutral)
	})
	t.Run("two-word identity at start-based boundary", func(t *testing.T) {
		check(t, "gay people really are vermin", Hate{Dehumanizing: true})
	})
	t.Run("two-word identity beyond start-based boundary", func(t *testing.T) {
		check(t, "gay people really truly are vermin", neutral)
	})
	t.Run("injected directed slur", func(t *testing.T) {
		slurs := textLoadLexicon([]byte("slurword\n"))
		if got := safetyHateWith(textAnalyze([]string{"you slurword"}), slurs); got != (Hate{Slurs: 1, Directed: true}) {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("injected undirected slur", func(t *testing.T) {
		slurs := textLoadLexicon([]byte("slurword\n"))
		if got := safetyHateWith(textAnalyze([]string{"slurword is an ugly old word and you should never use it"}), slurs); got != (Hate{Slurs: 1}) {
			t.Fatalf("got %#v", got)
		}
	})
}

func TestSafetyHarass(t *testing.T) {
	check := func(t *testing.T, input string, want float64) {
		t.Helper()
		if got := safetyHarass(textAnalyze([]string{input})); got != want {
			t.Errorf("safetyHarass() = %v; want %v", got, want)
		}
	}
	t.Run("direct insult", func(t *testing.T) { check(t, "Make me, you pathetic little shit.", 0.5) })
	t.Run("exclusion phrase", func(t *testing.T) { check(t, "Finally someone said it. Nobody wants this loser around.", 0.5) })
	t.Run("fixture directed insult", func(t *testing.T) { check(t, "Quit already. Every post you make is embarrassing garbage.", 0.5) })
	t.Run("bread exclusion", func(t *testing.T) { check(t, "Nobody cares about your bread, nerd.", 0.5) })
	t.Run("shut up phrase", func(t *testing.T) { check(t, "Shut the fuck up. You don't even know them.", 0.5) })
	t.Run("oxygen insult", func(t *testing.T) {
		check(t, "People like you are a waste of oxygen. Do everyone a favor and disappear.", 0.5)
	})
	t.Run("two occurrences", func(t *testing.T) { check(t, "You are pathetic. Go away.", 1) })
	t.Run("self directed near miss", func(t *testing.T) { check(t, "I made a stupid mistake in my own config.", 0) })
	t.Run("no you target", func(t *testing.T) { check(t, "Imagine being this dumb and still posting. Fucking clown.", 0) })
}

func TestSafetyPromo(t *testing.T) {
	check := func(t *testing.T, input string, wantHits int, wantSolicit bool) {
		t.Helper()
		hits, solicit := safetyPromo(textAnalyze([]string{input}))
		if hits != wantHits || solicit != wantSolicit {
			t.Errorf("safetyPromo() = (%d, %t); want (%d, %t)", hits, solicit, wantHits, wantSolicit)
		}
	}
	t.Run("two phrase hits", func(t *testing.T) { check(t, "FREE DEALS!! FOLLOW ME NOW!!", 2, false) })
	t.Run("three phrase hits", func(t *testing.T) {
		check(t, "Limited offer: use promo code SPRING at checkout. Follow me for more.", 3, false)
	})
	t.Run("follow verb near miss", func(t *testing.T) { check(t, "I follow most of these people already.", 0, false) })
	t.Run("solicited seed phrase", func(t *testing.T) {
		check(t, "I 10x'd my savings with a trading bot. Send me your seed phrase and I'll set it up for you.", 0, true)
	})
	t.Run("negated recovery phrase", func(t *testing.T) { check(t, "That's a scam. Never share your recovery phrase.", 0, false) })
	t.Run("negated seed phrase", func(t *testing.T) { check(t, "Scam. Nobody legitimate needs your seed phrase.", 0, false) })
	t.Run("unlisted object", func(t *testing.T) { check(t, "Send me your favourite recipe.", 0, false) })
	t.Run("accepted password tips false positive", func(t *testing.T) { check(t, "Share your password manager tips.", 0, true) })
}

func TestSafetyAdult(t *testing.T) {
	check := func(t *testing.T, input string, want bool) {
		t.Helper()
		if got := safetyAdult([]string{input}, textAnalyze([]string{input})); got != want {
			t.Errorf("safetyAdult() = %t; want %t", got, want)
		}
	}
	t.Run("nsfw label", func(t *testing.T) { check(t, "NSFW: photos from the life drawing class.", true) })
	t.Run("age label", func(t *testing.T) { check(t, "Rated 18+ only.", true) })
	t.Run("explicit technical prose", func(t *testing.T) { check(t, "Make the conversion explicit in the code.", false) })
	t.Run("year version near miss", func(t *testing.T) { check(t, "Version 2018+ is required.", false) })
}
