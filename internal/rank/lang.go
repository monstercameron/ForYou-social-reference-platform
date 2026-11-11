package rank

import (
	"embed"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

//go:embed lexicon/stopwords-de.txt lexicon/stopwords-en.txt lexicon/stopwords-es.txt lexicon/stopwords-fr.txt lexicon/stopwords-it.txt lexicon/stopwords-nl.txt lexicon/stopwords-pt.txt lexicon/topics.txt
var langLexicons embed.FS

type langStopwordList struct {
	code  string
	words map[string]struct{}
}

type langTopicEntry struct {
	name string
	hits int
}

var langStopwords []langStopwordList
var langTopicWords map[string]map[string]struct{}
var langTopicNames []string
var langScriptNames []string

func init() {
	for _, code := range []string{"de", "en", "es", "fr", "it", "nl", "pt"} {
		data, err := langLexicons.ReadFile("lexicon/stopwords-" + code + ".txt")
		if err != nil {
			panic(err)
		}
		words := langParseList(data)
		wordSet := make(map[string]struct{}, len(words))
		for _, word := range words {
			if !langWordValid(word) {
				panic("malformed embedded stopword")
			}
			wordSet[word] = struct{}{}
		}
		langStopwords = append(langStopwords, langStopwordList{code: code, words: wordSet})
	}

	topicData, err := langLexicons.ReadFile("lexicon/topics.txt")
	if err != nil {
		panic(err)
	}
	langTopicWords = langParseTopics(topicData)
	for name := range langTopicWords {
		langTopicNames = append(langTopicNames, name)
	}
	sort.Strings(langTopicNames)

	for name := range unicode.Scripts {
		langScriptNames = append(langScriptNames, name)
	}
	sort.Strings(langScriptNames)
}

func langParseList(data []byte) []string {
	if !utf8.Valid(data) || len(data) == 0 || data[len(data)-1] != '\n' || strings.ContainsRune(string(data), '\r') {
		panic("malformed embedded lexicon")
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if line == "" || line != strings.ToLower(line) {
			panic("malformed embedded lexicon")
		}
		if _, exists := seen[line]; exists {
			panic("duplicate embedded lexicon entry")
		}
		seen[line] = struct{}{}
	}
	return lines
}

func langWordValid(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func langParseTopics(data []byte) map[string]map[string]struct{} {
	lines := langParseList(data)
	topics := make(map[string]map[string]struct{}, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) != 2 || !langWordValid(parts[0]) || strings.Contains(parts[1], ":") {
			panic("malformed embedded topic lexicon")
		}
		words := strings.Split(parts[1], " ")
		wordSet := make(map[string]struct{}, len(words))
		for _, word := range words {
			if !langWordValid(word) || word != strings.ToLower(word) {
				panic("malformed embedded topic word")
			}
			if _, exists := wordSet[word]; exists {
				panic("duplicate embedded topic word")
			}
			wordSet[word] = struct{}{}
		}
		if len(words) < 2 {
			panic("malformed embedded topic lexicon")
		}
		if _, exists := topics[parts[0]]; exists {
			panic("duplicate embedded topic")
		}
		topics[parts[0]] = wordSet
	}
	return topics
}

// langLanguage identifies the best matching stopword list for rubric R15.
func langLanguage(words []string) string {
	if len(words) < 5 {
		return "und"
	}
	bestCode := "und"
	bestRate := 0.0
	for _, list := range langStopwords {
		hits := 0
		for _, word := range words {
			if _, found := list.words[word]; found {
				hits++
			}
		}
		rate := float64(hits) / float64(len(words))
		if rate > bestRate {
			bestRate = rate
			bestCode = list.code
		}
	}
	if bestRate < 0.12 {
		return "und"
	}
	return bestCode
}

// langScript returns the dominant Unicode script for rubric R16.
func langScript(prose []string) string {
	counts := make(map[string]int)
	previous := ""
	for _, text := range prose {
		for _, r := range text {
			if !unicode.IsLetter(r) {
				continue
			}
			if previous != "" && unicode.Is(unicode.Scripts[previous], r) {
				counts[previous]++
				continue
			}
			for _, name := range langScriptNames {
				if unicode.Is(unicode.Scripts[name], r) {
					counts[name]++
					previous = name
					break
				}
			}
		}
	}
	bestName := ""
	bestCount := 0
	for _, name := range langScriptNames {
		if counts[name] > bestCount {
			bestCount = counts[name]
			bestName = name
		}
	}
	return strings.ToLower(bestName)
}

// langTopics selects up to three matching rubric R17 topics deterministically.
func langTopics(words []string, hasCodeBlock bool) []string {
	tagged := make([]langTopicEntry, 0, len(langTopicNames))
	for _, name := range langTopicNames {
		hits := 0
		for _, word := range words {
			if _, found := langTopicWords[name][word]; found {
				hits++
				continue
			}
			if strings.HasSuffix(word, "s") {
				if _, found := langTopicWords[name][word[:len(word)-1]]; found {
					hits++
				}
			}
		}
		if name == "tech" && hasCodeBlock {
			hits++
		}
		if hits >= 2 || (hits >= 1 && len(words) < 25) {
			tagged = append(tagged, langTopicEntry{name: name, hits: hits})
		}
	}
	sort.Slice(tagged, func(i, j int) bool {
		if tagged[i].hits != tagged[j].hits {
			return tagged[i].hits > tagged[j].hits
		}
		return tagged[i].name < tagged[j].name
	})
	if len(tagged) > 3 {
		tagged = tagged[:3]
	}
	if len(tagged) == 0 {
		return nil
	}
	result := make([]string, len(tagged))
	for i, topic := range tagged {
		result[i] = topic.name
	}
	sort.Strings(result)
	return result
}
