package rank

import (
	_ "embed"
	"regexp"

	"golang.org/x/text/unicode/norm"
)

//go:embed lexicon/profanity-mild.txt
var safetyProfanityMildData string

//go:embed lexicon/profanity-strong.txt
var safetyProfanityStrongData string

//go:embed lexicon/threat-intent.txt
var safetyThreatIntentData string

//go:embed lexicon/threat-verbs.txt
var safetyThreatVerbsData string

//go:embed lexicon/threat-targets.txt
var safetyThreatTargetsData string

//go:embed lexicon/hate-identity.txt
var safetyHateIdentityData string

//go:embed lexicon/hate-dehumanizing.txt
var safetyHateDehumanizingData string

//go:embed lexicon/hate-slurs.txt
var safetyHateSlursData string

//go:embed lexicon/harass-insults.txt
var safetyHarassInsultsData string

//go:embed lexicon/harass-exclusion.txt
var safetyHarassExclusionData string

//go:embed lexicon/promo.txt
var safetyPromoData string

//go:embed lexicon/solicit-objects.txt
var safetySolicitObjectsData string

//go:embed lexicon/adult.txt
var safetyAdultData string

var (
	safetyMild           = textLoadLexicon([]byte(safetyProfanityMildData))
	safetyStrong         = textLoadLexicon([]byte(safetyProfanityStrongData))
	safetyIntent         = textLoadLexicon([]byte(safetyThreatIntentData))
	safetyVerbs          = textLoadLexicon([]byte(safetyThreatVerbsData))
	safetyTargets        = textLoadLexicon([]byte(safetyThreatTargetsData))
	safetyIdentities     = textLoadLexicon([]byte(safetyHateIdentityData))
	safetyDehumanizing   = textLoadLexicon([]byte(safetyHateDehumanizingData))
	safetySlurs          = textLoadLexicon([]byte(safetyHateSlursData))
	safetyInsults        = textLoadLexicon([]byte(safetyHarassInsultsData))
	safetyExclusion      = textLoadLexicon([]byte(safetyHarassExclusionData))
	safetyPromos         = textLoadLexicon([]byte(safetyPromoData))
	safetySolicitObjects = textLoadLexicon([]byte(safetySolicitObjectsData))
	safetyAdults         = textLoadLexicon([]byte(safetyAdultData))
	safetyAdultPattern   = regexp.MustCompile(`(?:^|[^0-9])18\+`)
)

const (
	safetyCopulaWords      = "are\nis\nwere\nwas\n"
	safetyHateYouWordsText = "you\nyou're\nyoure\nyour\nur\nu\n"
	safetyHarassYouText    = "you\nyou're\nyoure\n"
	safetySolicitVerbText  = "send\nshare\nenter\ngive\ndm\npaste\npost\ntype\n"
	safetyNegationText     = "never\ndon't\ndont\nnot\n"
)

var (
	safetyCopulas        = textLoadLexicon([]byte(safetyCopulaWords))
	safetyHateYouWords   = textLoadLexicon([]byte(safetyHateYouWordsText))
	safetyHarassYouWords = textLoadLexicon([]byte(safetyHarassYouText))
	safetySolicitVerbs   = textLoadLexicon([]byte(safetySolicitVerbText))
	safetyNegations      = textLoadLexicon([]byte(safetyNegationText))
)

// Hate reports the safety features defined by rubric R11.
type Hate struct {
	Slurs        int  // Slurs is the number of matched slur entries.
	Directed     bool // Directed reports a nearby hate you-word.
	Dehumanizing bool // Dehumanizing reports an identity plus copula and noun match.
}

func safetyProfanity(input textInput) (mild, strong int) {
	for _, word := range input.words {
		normalized := textNorm(word)
		if textLexiconMatchesWord(normalized, safetyStrong) {
			strong++
		} else if textLexiconMatchesWord(normalized, safetyMild) {
			mild++
		}
	}
	return mild, strong
}

func safetyThreat(input textInput) float64 {
	var maximum float64
	for _, sentence := range input.sentences {
		normalized := textNormSentence(sentence)
		intent := textCountNormalizedMatches(normalized, safetyIntent) > 0
		verb := textCountNormalizedMatches(normalized, safetyVerbs) > 0
		target := textCountNormalizedMatches(normalized, safetyTargets) > 0
		if verb && target {
			if intent {
				return 1
			}
			maximum = 0.5
		}
	}
	return maximum
}

func safetyHate(input textInput) Hate {
	return safetyHateWith(input, safetySlurs)
}

func safetyHateWith(input textInput, slurs textLexicon) Hate {
	var result Hate
	for _, words := range input.sentences {
		sentence := textNormSentence(words)
		for start := range sentence {
			if textPhraseMatchesAny(sentence, slurs, start) {
				result.Slurs++
				if textHasPhraseWithin(sentence, safetyHateYouWords, start, 4) {
					result.Directed = true
				}
			}
			if textPhraseMatchesAny(sentence, safetyIdentities, start) {
				for copula := start + 1; copula <= start+3 && copula < len(sentence); copula++ {
					if !textPhraseMatchesAny(sentence, safetyCopulas, copula) {
						continue
					}
					for noun := copula + 1; noun <= copula+3 && noun < len(sentence); noun++ {
						if textPhraseMatchesAny(sentence, safetyDehumanizing, noun) {
							result.Dehumanizing = true
						}
					}
				}
			}
		}
	}
	return result
}

func safetyHarass(input textInput) float64 {
	hits := 0
	for _, words := range input.sentences {
		sentence := textNormSentence(words)
		for start := range sentence {
			if textPhraseMatchesAny(sentence, safetyInsults, start) && textHasPhraseWithin(sentence, safetyHarassYouWords, start, 4) {
				hits++
			}
		}
		hits += textCountNormalizedMatches(sentence, safetyExclusion)
	}
	if hits > 2 {
		hits = 2
	}
	return float64(hits) * 0.5
}

func safetyPromo(input textInput) (hits int, solicitSecret bool) {
	for _, words := range input.sentences {
		sentence := textNormSentence(words)
		hits += textCountNormalizedMatches(sentence, safetyPromos)
		for verb := range sentence {
			if !textPhraseMatchesAny(sentence, safetySolicitVerbs, verb) {
				continue
			}
			negated := false
			for position := verb - 3; position < verb; position++ {
				if position >= 0 && textPhraseMatchesAny(sentence, safetyNegations, position) {
					negated = true
				}
			}
			if negated {
				continue
			}
			for your := verb + 1; your <= verb+3 && your < len(sentence); your++ {
				if sentence[your] == "your" && textPhraseMatchesAny(sentence, safetySolicitObjects, your+1) {
					solicitSecret = true
				}
			}
		}
	}
	return hits, solicitSecret
}

func safetyAdult(prose []string, input textInput) bool {
	for _, words := range input.sentences {
		if textCountNormalizedMatches(textNormSentence(words), safetyAdults) > 0 {
			return true
		}
	}
	for _, raw := range prose {
		if safetyAdultPattern.MatchString(norm.NFKC.String(raw)) {
			return true
		}
	}
	return false
}
