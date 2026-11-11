package rank

import (
	"math"
	"sort"
	"time"
)

const (
	// HalfLife is the exponential decay half-life for votes.
	HalfLife = 90 * 24 * time.Hour
	// PriorStrength is the number of prior pseudo-votes per indicator.
	PriorStrength = 4.0
	// Lambda damps the sum of indicator weights.
	Lambda = 0.5
	// WeightLimit is the absolute clamp for an indicator weight.
	WeightLimit = 3.0
	// MinVotes is the decayed vote count required to activate a model.
	MinVotes = 10.0
	// MinLikes is the decayed like count required to activate a model.
	MinLikes = 2.0
	// MinDislikes is the decayed dislike count required to activate a model.
	MinDislikes = 2.0
	// MaxVotes is the profile store's vote bound; Train does not truncate.
	MaxVotes = 2000
)

// Vote is one like or dislike with the post's indicators at voting time.
type Vote struct {
	PostID     string
	AuthorID   string
	Like       bool
	At         time.Time
	Indicators []string
}

// Model is a reader's taste model derived from their votes. The zero Model
// behaves like a model trained on no votes.
type Model struct {
	p0       float64
	likes    float64
	dislikes float64
	weights  map[string]Weight
}

// Weight is one indicator's evidence and weight.
type Weight struct {
	Indicator          string
	Likes, Dislikes, W float64
	Muted              bool
}

// Score is the model's estimate for one post.
type Score struct {
	P, Z, Support float64
	Top           []Weight // At most three, by |W| descending then Indicator ascending; Why? shows Lambda * W.
}

type modelCounts struct {
	likes    float64
	dislikes float64
}

// Train derives a taste model from votes as of now and applies the reader's mutes.
func Train(votes []Vote, now time.Time, muted map[string]bool) Model {
	ordered := append([]Vote(nil), votes...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.PostID != b.PostID {
			return a.PostID < b.PostID
		}
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.Like != b.Like {
			return a.Like
		}
		if a.AuthorID != b.AuthorID {
			return a.AuthorID < b.AuthorID
		}
		return modelIndicatorsLess(a.Indicators, b.Indicators)
	})

	counted := make([]Vote, 0, len(ordered))
	for i := 0; i < len(ordered); {
		j := i + 1
		for j < len(ordered) && ordered[j].PostID == ordered[i].PostID {
			j++
		}
		counted = append(counted, ordered[j-1])
		i = j
	}
	sort.Slice(counted, func(i, j int) bool {
		if !counted[i].At.Equal(counted[j].At) {
			return counted[i].At.Before(counted[j].At)
		}
		return counted[i].PostID < counted[j].PostID
	})

	indicatorCounts := make(map[string]modelCounts)
	var likes, dislikes float64
	for _, vote := range counted {
		age := now.Sub(vote.At)
		if age < 0 {
			age = 0
		}
		decay := math.Exp2(-age.Seconds() / HalfLife.Seconds())
		if vote.Like {
			likes += decay
		} else {
			dislikes += decay
		}
		indicators := modelUniqueSorted(vote.Indicators)
		for _, indicator := range indicators {
			counts := indicatorCounts[indicator]
			if vote.Like {
				counts.likes += decay
			} else {
				counts.dislikes += decay
			}
			indicatorCounts[indicator] = counts
		}
	}

	p0 := (likes + 1) / (likes + dislikes + 2)
	weights := make(map[string]Weight, len(indicatorCounts)+len(muted))
	for indicator, counts := range indicatorCounts {
		n := counts.likes + counts.dislikes
		posterior := (counts.likes + PriorStrength*p0) / (n + PriorStrength)
		w := math.Log(posterior/(1-posterior)) - math.Log(p0/(1-p0))
		w = math.Max(-WeightLimit, math.Min(WeightLimit, w))
		isMuted := muted[indicator]
		if isMuted {
			w = 0
		}
		weights[indicator] = Weight{
			Indicator: indicator,
			Likes:     counts.likes,
			Dislikes:  counts.dislikes,
			W:         w,
			Muted:     isMuted,
		}
	}
	for indicator, isMuted := range muted {
		if isMuted {
			if _, exists := weights[indicator]; !exists {
				weights[indicator] = Weight{Indicator: indicator, Muted: true}
			}
		}
	}
	return Model{p0: p0, likes: likes, dislikes: dislikes, weights: weights}
}

// Active reports whether the decayed vote counts meet the cold-start thresholds.
func (m Model) Active() bool {
	return m.likes+m.dislikes >= MinVotes && m.likes >= MinLikes && m.dislikes >= MinDislikes
}

// BaseRate returns the reader's smoothed overall like rate.
func (m Model) BaseRate() float64 {
	if m.p0 == 0 {
		return 0.5
	}
	return m.p0
}

// Weights returns all learned and explicitly muted indicator rows in name order.
func (m Model) Weights() []Weight {
	result := make([]Weight, 0, len(m.weights))
	for _, weight := range m.weights {
		result = append(result, weight)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Indicator < result[j].Indicator
	})
	return result
}

// Score estimates the probability of liking a post with the given indicators.
func (m Model) Score(indicators []string) Score {
	var z, support float64
	var top []Weight
	for _, indicator := range modelUniqueSorted(indicators) {
		weight := m.weights[indicator]
		z += Lambda * weight.W
		if !weight.Muted {
			support += weight.Likes + weight.Dislikes
		}
		if weight.W != 0 {
			top = append(top, weight)
		}
	}
	sort.Slice(top, func(i, j int) bool {
		a, b := math.Abs(top[i].W), math.Abs(top[j].W)
		if a != b {
			return a > b
		}
		return top[i].Indicator < top[j].Indicator
	})
	if len(top) > 3 {
		top = top[:3]
	}
	if len(top) == 0 {
		top = nil
	}
	return Score{P: 1 / (1 + math.Exp(-z)), Z: z, Support: support, Top: top}
}

func modelUniqueSorted(indicators []string) []string {
	result := append([]string(nil), indicators...)
	sort.Strings(result)
	if len(result) < 2 {
		return result
	}
	unique := result[:1]
	for _, indicator := range result[1:] {
		if indicator != unique[len(unique)-1] {
			unique = append(unique, indicator)
		}
	}
	return unique
}

func modelIndicatorsLess(a, b []string) bool {
	aSorted, bSorted := modelUniqueSorted(a), modelUniqueSorted(b)
	for i := 0; i < len(aSorted) && i < len(bSorted); i++ {
		if aSorted[i] != bSorted[i] {
			return aSorted[i] < bSorted[i]
		}
	}
	return len(aSorted) < len(bSorted)
}
