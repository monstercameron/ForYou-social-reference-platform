package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/rank"
	"foryou.local/foryou/internal/wire"
)

// A dimension is one thing the rubric rates in a post that a reader may not
// want to see. Its levels run from none to the most severe, four at most. The
// reader chooses, per dimension, the highest level shown without folding.
type dimension struct {
	Key, Name, About string
	Levels           []string
	Shown            int
	level            func(rank.Features) int
}

var dimensions = []dimension{
	{Key: "profanity", Name: "Profanity", About: "Swearing and crude words.", Levels: []string{"None", "Mild", "Strong", "Heavy"}, Shown: 3, level: profanityLevel},
	{Key: "threats", Name: "Threats", About: "Language that threatens violence.", Levels: []string{"None", "Indirect", "Direct"}, Shown: 0, level: threatLevel},
	{Key: "hate", Name: "Hate", About: "Slurs and attacks on groups of people.", Levels: []string{"None", "Slur", "Directed", "Dehumanizing"}, Shown: 0, level: hateLevel},
	{Key: "harassment", Name: "Harassment", About: "Insults and pile-ons aimed at a person.", Levels: []string{"None", "Mild", "Strong", "Severe"}, Shown: 1, level: harassmentLevel},
	{Key: "spam", Name: "Spam and shouting", About: "Capitals, repetition and floods of symbols.", Levels: []string{"None", "Loud", "Repetitive", "Extreme"}, Shown: 2, level: spamLevel},
	{Key: "promotion", Name: "Promotion", About: "Selling, and requests for wallet or password details.", Levels: []string{"None", "Promotional", "Pushy", "Asks for secrets"}, Shown: 2, level: promotionLevel},
	{Key: "adult", Name: "Adult", About: "Posts their author labelled as adult content.", Levels: []string{"None", "Labelled adult"}, Shown: 0, level: adultLevel},
	{Key: "secrets", Name: "Secrets", About: "Text shaped like a key, token or password.", Levels: []string{"None", "Credential-shaped"}, Shown: 0, level: secretLevel},
}

func profanityLevel(f rank.Features) int {
	switch p := f.Profanity; {
	case p == nil:
		return 0
	case p.Strong >= 2:
		return 3
	case p.Strong == 1:
		return 2
	case p.Mild > 0:
		return 1
	}
	return 0
}

func threatLevel(f rank.Features) int {
	switch {
	case f.Threat >= 1:
		return 2
	case f.Threat > 0:
		return 1
	}
	return 0
}

func hateLevel(f rank.Features) int {
	switch {
	case f.Hate.Dehumanizing:
		return 3
	case f.Hate.Directed:
		return 2
	case f.Hate.Slurs > 0:
		return 1
	}
	return 0
}

func harassmentLevel(f rank.Features) int {
	switch {
	case f.Harass >= 1:
		return 3
	case f.Harass >= 0.5:
		return 2
	case f.Harass > 0:
		return 1
	}
	return 0
}

func spamLevel(f rank.Features) int {
	loud := f.CapsRatio >= 0.7 || f.BangRuns >= 2 || f.Repetition.EmojiRatio >= 0.3
	repetitive := f.Repetition.Rep3 >= 0.4 || f.Repetition.CharRepeat >= 0.5
	switch {
	case f.CapsRatio >= 0.7 && repetitive:
		return 3
	case repetitive:
		return 2
	case loud:
		return 1
	}
	return 0
}

func promotionLevel(f rank.Features) int {
	switch {
	case f.SolicitSecret:
		return 3
	case f.PromoHits >= 3:
		return 2
	case f.PromoHits > 0:
		return 1
	}
	return 0
}

func adultLevel(f rank.Features) int {
	if f.AdultLabel {
		return 1
	}
	return 0
}

func secretLevel(f rank.Features) int {
	if len(f.Secrets) > 0 {
		return 1
	}
	return 0
}

// ratedText is what the rubric says about one text. It depends on the text
// alone, so it is worked out once and kept.
type ratedText struct {
	levels           []int
	language, length string
	topics           string
}

var ratedTexts sync.Map

func rateText(format, text string) ratedText {
	key := format + "\x00" + text
	if kept, ok := ratedTexts.Load(key); ok {
		return kept.(ratedText)
	}
	rated := ratedText{levels: make([]int, len(dimensions))}
	segments, err := content.Segment(wire.PostPayload{ContentFormat: format, Text: text})
	if err == nil {
		features := rank.Extract(rank.Post{Segments: segments, BillableBytes: len(text)}, time.Time{})
		for i, d := range dimensions {
			rated.levels[i] = d.level(features)
		}
		rated.language, rated.length, rated.topics = features.Language, string(features.Length), strings.Join(features.Topics, ", ")
	}
	ratedTexts.Store(key, rated)
	return rated
}

type ratingLine struct {
	Name, Level string
	// Severity places the level on a common scale of 0 to 3, whatever the number of levels.
	Severity int
	Over     bool
}

// rating is a post's rubric rating as one reader sees it: every dimension,
// the ones above none, and the ones above the reader's own filters.
type rating struct {
	Lines, Flags, Over      []ratingLine
	Language, Length, Topic string
}

func rate(format, text string, shown []int) rating {
	rated := rateText(format, text)
	result := rating{Lines: make([]ratingLine, len(dimensions)), Language: rated.language, Length: rated.length, Topic: rated.topics}
	for i, d := range dimensions {
		level := rated.levels[i]
		line := ratingLine{Name: d.Name, Level: d.Levels[level], Severity: level * 3 / (len(d.Levels) - 1), Over: level > shown[i]}
		result.Lines[i] = line
		if level > 0 {
			result.Flags = append(result.Flags, line)
		}
		if line.Over {
			result.Over = append(result.Over, line)
		}
	}
	return result
}

const filterCookie = "foryou_filters"

// readerFilters returns, per dimension, the highest level this reader sees
// unfolded. The choice lives in the browser: one digit per dimension.
func readerFilters(r *http.Request) []int {
	shown := make([]int, len(dimensions))
	for i, d := range dimensions {
		shown[i] = d.Shown
	}
	c, err := r.Cookie(filterCookie)
	if err != nil || len(c.Value) != len(dimensions) {
		return shown
	}
	chosen := make([]int, len(dimensions))
	for i, d := range dimensions {
		chosen[i] = int(c.Value[i] - '0')
		if chosen[i] < 0 || chosen[i] >= len(d.Levels) {
			return shown
		}
	}
	return chosen
}

type filterOption struct {
	Value    int
	Label    string
	Selected bool
}

type filterRow struct {
	Key, Name, About string
	Options          []filterOption
}

func filterRows(shown []int) []filterRow {
	rows := make([]filterRow, len(dimensions))
	for i, d := range dimensions {
		rows[i] = filterRow{Key: d.Key, Name: d.Name, About: d.About, Options: make([]filterOption, len(d.Levels))}
		for level := range d.Levels {
			label := "Up to " + strings.ToLower(d.Levels[level])
			switch {
			case len(d.Levels) == 2 && level == 0:
				label = "Fold"
			case level == 0:
				label = "Fold any"
			case level == len(d.Levels)-1:
				label = "Show all"
			}
			rows[i].Options[level] = filterOption{Value: level, Label: label, Selected: shown[i] == level}
		}
	}
	return rows
}

type filterSettings struct {
	Filters []filterRow
	Saved   bool
	Rubric  string
}

func filterSettingsFor(shown []int, saved bool) filterSettings {
	return filterSettings{Filters: filterRows(shown), Saved: saved, Rubric: rank.RubricVersion}
}

// filtersHandler saves the reader's content filters, or puts them back to the defaults.
func (a *App) filtersHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r)
	if !ok {
		return
	}
	cookie := &http.Cookie{Name: filterCookie, Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode}
	shown := readerFilters(r)
	if form.Get("reset") != "" {
		cookie.MaxAge = -1
		shown = readerFilters(&http.Request{})
	} else {
		value := make([]byte, len(dimensions))
		for i, d := range dimensions {
			if choice := form.Get(d.Key); len(choice) == 1 && choice[0] >= '0' && int(choice[0]-'0') < len(d.Levels) {
				shown[i] = int(choice[0] - '0')
			} else if choice != "" {
				http.Error(w, "invalid filter", http.StatusBadRequest)
				return
			}
			value[i] = byte('0' + shown[i])
		}
		cookie.Value = string(value)
	}
	http.SetCookie(w, cookie)
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/settings/filters/fragment" {
		http.Redirect(w, r, "/settings#content-filters", http.StatusSeeOther)
		return
	}
	a.fragment(w, r, "settings-filters", filterSettingsFor(shown, true))
}

func rateReplies(replies []reply, shown []int) {
	for i := range replies {
		replies[i].Rating = rate(content.FormatMarkdown, replies[i].Text, shown)
		rateReplies(replies[i].Children, shown)
	}
}
