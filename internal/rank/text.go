package rank

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

type textInput struct {
	sentences [][]string // Lowercased words, one slice per non-empty sentence.
	words     []string   // All words in sentence order.
}

type textLexicon [][]string

func textLoadLexicon(data []byte) textLexicon {
	if len(data) == 0 || data[len(data)-1] != '\n' || !utf8.Valid(data) {
		panic("embedded lexicon must end with newline")
	}
	var entries textLexicon
	for _, line := range strings.Split(string(data[:len(data)-1]), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if line == "" || strings.ContainsRune(line, '\r') || !norm.NFC.IsNormalString(line) || strings.ToLower(line) != line {
			panic("malformed embedded lexicon entry")
		}
		words := strings.Split(line, " ")
		for _, word := range words {
			if word == "" {
				panic("malformed embedded lexicon phrase")
			}
			var previous rune
			run := 0
			for _, r := range word {
				if unicode.IsSpace(r) {
					panic("embedded lexicon entry contains non-separator whitespace")
				}
				if r == previous {
					run++
				} else {
					previous, run = r, 1
				}
				if r != '*' && run >= 3 {
					panic("embedded lexicon entry has repeated run")
				}
			}
		}
		entries = append(entries, words)
	}
	return entries
}

func textAnalyze(prose []string) textInput {
	var result textInput
	for _, raw := range prose {
		text := strings.ReplaceAll(norm.NFKC.String(raw), "\u2019", "'")
		start := 0
		for i := 0; i < len(text); {
			r, size := rune(text[i]), 1
			if r >= 0x80 {
				r, size = utf8.DecodeRuneInString(text[i:])
			}
			if !textIsTerminator(r) {
				i += size
				continue
			}
			end := i + size
			for end < len(text) {
				next, nextSize := utf8.DecodeRuneInString(text[end:])
				if !textIsTerminator(next) {
					break
				}
				end += nextSize
			}
			if end == len(text) || textIsSpaceAt(text, end) {
				textAppendSentence(&result, text[start:end])
				start = end
			}
			i = end
		}
		textAppendSentence(&result, text[start:])
	}
	return result
}

func textAppendSentence(input *textInput, sentence string) {
	var words []string
	var current strings.Builder
	textFlush := func() {
		if current.Len() == 0 {
			return
		}
		word := strings.Trim(current.String(), "'")
		current.Reset()
		if word == "" || !textHasLetterOrDigit(word) {
			return
		}
		words = append(words, strings.ToLower(word))
	}
	for _, r := range sentence {
		if textIsWordRune(r) {
			current.WriteRune(r)
		} else {
			textFlush()
		}
	}
	textFlush()
	if len(words) == 0 {
		return
	}
	input.sentences = append(input.sentences, words)
	input.words = append(input.words, words...)
}

func textIsWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) || r == '\'' || r == '*' || r == '@' || r == '$'
}

func textHasLetterOrDigit(word string) bool {
	for _, r := range word {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func textIsTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '…', '。', '！', '？':
		return true
	default:
		return false
	}
}

func textIsSpaceAt(value string, offset int) bool {
	r, _ := utf8.DecodeRuneInString(value[offset:])
	return unicode.IsSpace(r)
}

func textNorm(word string) string {
	runes := []rune(word)
	var mapped strings.Builder
	for _, r := range runes {
		switch r {
		case '0':
			r = 'o'
		case '1':
			r = 'i'
		case '3':
			r = 'e'
		case '4':
			r = 'a'
		case '5':
			r = 's'
		case '7':
			r = 't'
		case '@':
			r = 'a'
		case '$':
			r = 's'
		}
		mapped.WriteRune(r)
	}
	runes = []rune(mapped.String())
	var result strings.Builder
	for i, r := range runes {
		if i >= 2 && r != '*' && runes[i-1] == r && runes[i-2] == r {
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}

func textWordMatches(normWord, entry string) bool {
	actual := []rune(normWord)
	wanted := []rune(entry)
	if len(actual) != len(wanted) {
		return false
	}
	wildcards := 0
	for i, r := range actual {
		if r == '*' {
			wildcards++
			continue
		}
		if r != wanted[i] {
			return false
		}
	}
	return wildcards == 0 || (actual[0] != '*' && wildcards*2 <= len(actual))
}

func textNormSentence(sentence []string) []string {
	normalized := make([]string, len(sentence))
	for i, word := range sentence {
		normalized[i] = textNorm(word)
	}
	return normalized
}

func textPhraseMatches(sentence []string, phrase []string, start int) bool {
	if start < 0 || start+len(phrase) > len(sentence) {
		return false
	}
	for i, word := range phrase {
		if !textWordMatches(sentence[start+i], word) {
			return false
		}
	}
	return true
}

func textCountMatches(sentence []string, lexicon textLexicon) int {
	return textCountNormalizedMatches(textNormSentence(sentence), lexicon)
}

func textCountNormalizedMatches(sentence []string, lexicon textLexicon) int {
	count := 0
	for start := range sentence {
		for _, phrase := range lexicon {
			if textPhraseMatches(sentence, phrase, start) {
				count++
			}
		}
	}
	return count
}

func textLexiconMatchesWord(normWord string, lexicon textLexicon) bool {
	for _, entry := range lexicon {
		if len(entry) == 1 && textWordMatches(normWord, entry[0]) {
			return true
		}
	}
	return false
}

func textPhraseMatchesAny(sentence []string, lexicon textLexicon, start int) bool {
	for _, phrase := range lexicon {
		if textPhraseMatches(sentence, phrase, start) {
			return true
		}
	}
	return false
}

func textHasPhraseWithin(sentence []string, lexicon textLexicon, position, distance int) bool {
	for start := position - distance; start <= position+distance; start++ {
		if start >= 0 && textPhraseMatchesAny(sentence, lexicon, start) {
			return true
		}
	}
	return false
}
