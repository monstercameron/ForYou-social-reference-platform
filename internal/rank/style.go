package rank

import "unicode"

// Repetition is rubric R14.
type Repetition struct {
	Rep3       float64
	CharRepeat float64
	Emoji      int
	EmojiRatio float64
}

// styleShouting computes rubric R13: uppercase share and long punctuation runs.
func styleShouting(prose []string) (capsRatio float64, bangRuns int) {
	var upper, cased int
	for _, text := range prose {
		bangLength := 0
		for _, r := range text {
			if unicode.IsUpper(r) {
				upper++
				cased++
			} else if unicode.IsLower(r) {
				cased++
			}

			if r == '!' || r == '?' {
				bangLength++
				continue
			}
			if bangLength >= 3 {
				bangRuns++
			}
			bangLength = 0
		}
		if bangLength >= 3 {
			bangRuns++
		}
	}
	if cased >= 20 {
		capsRatio = float64(upper) / float64(cased)
	}
	return capsRatio, bangRuns
}

// styleRepetition computes rubric R14: repeated trigrams, character stretches, and emoji.
func styleRepetition(prose, words []string) Repetition {
	var result Repetition
	totalTrigrams := len(words) - 2
	if totalTrigrams >= 6 {
		trigrams := make(map[[3]string]struct{}, totalTrigrams)
		for i := 0; i < totalTrigrams; i++ {
			trigrams[[3]string{words[i], words[i+1], words[i+2]}] = struct{}{}
		}
		result.Rep3 = 1 - float64(len(trigrams))/float64(totalTrigrams)
	}

	var allRunes, nonSpaceRunes, longestStretch int
	for _, text := range prose {
		runes := []rune(text)
		allRunes += len(runes)
		prefixNonSpace := make([]int, len(runes)+1)
		for i, r := range runes {
			prefixNonSpace[i+1] = prefixNonSpace[i]
			if !unicode.IsSpace(r) {
				nonSpaceRunes++
				prefixNonSpace[i+1]++
			}
			if styleEmoji(r) {
				result.Emoji++
			}
		}
		for period := 1; period <= 8; period++ {
			run := 0
			for i := period; i < len(runes); i++ {
				if runes[i] != runes[i-period] {
					run = 0
					continue
				}
				run++
				length := period + run
				if length < 4*period {
					continue
				}
				start := i - length + 1
				if prefixNonSpace[i+1] == prefixNonSpace[start] {
					continue
				}
				if length > longestStretch {
					longestStretch = length
				}
			}
		}
	}
	if allRunes > 0 {
		result.CharRepeat = float64(longestStretch) / float64(allRunes)
	}
	if nonSpaceRunes > 0 {
		result.EmojiRatio = float64(result.Emoji) / float64(nonSpaceRunes)
	}
	return result
}
