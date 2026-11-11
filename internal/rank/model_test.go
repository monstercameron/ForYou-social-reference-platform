package rank

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"
)

type modelVectorCase struct {
	Votes []Vote              `json:"votes"`
	Posts map[string][]string `json:"posts"`
}

func modelReadVectors(t *testing.T) map[string]modelVectorCase {
	t.Helper()
	data, err := os.ReadFile("testdata/model-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors map[string]modelVectorCase
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func modelCheckFloat(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %.9f; want %.9f", label, got, want)
	}
}

func modelCheckScore(t *testing.T, label string, got Score, z, p, support float64, top []string) {
	t.Helper()
	modelCheckFloat(t, label+" Z", got.Z, z)
	modelCheckFloat(t, label+" P", got.P, p)
	modelCheckFloat(t, label+" Support", got.Support, support)
	if len(got.Top) != len(top) {
		t.Errorf("%s Top has %d entries; want %d", label, len(got.Top), len(top))
		return
	}
	if len(top) > 0 && got.Top[0].Indicator != top[0] {
		t.Errorf("%s Top[0] = %q; want %q", label, got.Top[0].Indicator, top[0])
	}
	if len(top) > 1 && got.Top[1].Indicator != top[1] {
		t.Errorf("%s Top[1] = %q; want %q", label, got.Top[1].Indicator, top[1])
	}
	if len(top) > 2 && got.Top[2].Indicator != top[2] {
		t.Errorf("%s Top[2] = %q; want %q", label, got.Top[2].Indicator, top[2])
	}
}

func modelCheckScoreNumbers(t *testing.T, got Score, z, p, support float64) {
	t.Helper()
	modelCheckFloat(t, "Z", got.Z, z)
	modelCheckFloat(t, "P", got.P, p)
	modelCheckFloat(t, "Support", got.Support, support)
}

func modelFindWeight(t *testing.T, weights []Weight, indicator string) Weight {
	t.Helper()
	index := slices.IndexFunc(weights, func(weight Weight) bool { return weight.Indicator == indicator })
	if index < 0 {
		t.Fatalf("Weights() has no %q row", indicator)
	}
	return weights[index]
}

func modelCheckWeight(t *testing.T, weights []Weight, index int, indicator string, likes, dislikes, w float64) {
	t.Helper()
	if weights[index].Indicator != indicator {
		t.Errorf("Weights()[%d].Indicator = %q; want %q", index, weights[index].Indicator, indicator)
	}
	modelCheckFloat(t, indicator+" likes", weights[index].Likes, likes)
	modelCheckFloat(t, indicator+" dislikes", weights[index].Dislikes, dislikes)
	modelCheckFloat(t, indicator+" W", weights[index].W, w)
	if weights[index].Muted {
		t.Errorf("Weights()[%d] is unexpectedly muted", index)
	}
}

func TestModelHistory(t *testing.T) {
	vectors := modelReadVectors(t)
	v := vectors["history"]
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := Train(v.Votes, now, nil)
	t.Run("history weights", func(t *testing.T) {
		if got := len(m.Weights()); got != 20 {
			t.Fatalf("Weights() has %d rows; want 20", got)
		}
		modelCheckFloat(t, "BaseRate", m.BaseRate(), 0.5)
		if !m.Active() {
			t.Fatal("Active() = false; want true")
		}
		w := m.Weights()
		modelCheckWeight(t, w, 0, "code:block", 2, 0, 0.693147)
		modelCheckWeight(t, w, 1, "harass", 0, 1, -0.405465)
		modelCheckWeight(t, w, 2, "key:A", 2, 0, 0.693147)
		modelCheckWeight(t, w, 3, "key:B", 0, 1, -0.405465)
		modelCheckWeight(t, w, 4, "lang:en", 6, 6, 0)
		modelCheckWeight(t, w, 5, "len:medium", 2, 1, 0.287682)
		modelCheckWeight(t, w, 6, "len:short", 4, 4, 0)
		modelCheckWeight(t, w, 7, "len:tiny", 0, 1, -0.405465)
		modelCheckWeight(t, w, 8, "link:any", 1, 1, 0)
		modelCheckWeight(t, w, 9, "media:image", 1, 0, 0.405465)
		modelCheckWeight(t, w, 10, "prof:mild", 0, 1, -0.405465)
		modelCheckWeight(t, w, 11, "prof:strong", 0, 2, -0.693147)
		modelCheckWeight(t, w, 12, "promo", 0, 3, -0.916291)
		modelCheckWeight(t, w, 13, "spam:caps", 0, 2, -0.693147)
		modelCheckWeight(t, w, 14, "spam:repeat", 0, 1, -0.405465)
		modelCheckWeight(t, w, 15, "thread:deep", 1, 0, 0.405465)
		modelCheckWeight(t, w, 16, "thread:reply", 2, 3, -0.223144)
		modelCheckWeight(t, w, 17, "topic:food", 1, 0, 0.405465)
		modelCheckWeight(t, w, 18, "topic:garden", 1, 0, 0.405465)
		modelCheckWeight(t, w, 19, "topic:tech", 3, 0, 0.916291)
	})
	t.Run("history scores", func(t *testing.T) {
		modelCheckScore(t, "P1", m.Score(v.Posts["P1"]), 1.242453, 0.775991, 33, []string{"topic:tech", "code:block", "key:A"})
		modelCheckFloat(t, "P1 first top W", m.Score(v.Posts["P1"]).Top[0].W, 0.916291)
		modelCheckScore(t, "P2", m.Score(v.Posts["P2"]), -0.458145, 0.387426, 27, []string{"prof:strong", "thread:reply"})
		modelCheckScore(t, "P3", m.Score(v.Posts["P3"]), -0.111572, 0.472136, 25, []string{"thread:reply"})
		modelCheckScore(t, "P4", m.Score(v.Posts["P4"]), 0.693147, 0.666667, 30, []string{"topic:tech", "code:block", "thread:reply"})
		modelCheckScore(t, "P5", m.Score(v.Posts["P5"]), 0, 0.5, 20, nil)
		modelCheckScore(t, "P6", m.Score(v.Posts["P6"]), -1.119023, 0.246192, 23, []string{"promo", "spam:caps", "len:tiny"})
		modelCheckScore(t, "P7", m.Score(v.Posts["P7"]), -0.111572, 0.472136, 19, []string{"len:tiny", "thread:deep", "thread:reply"})
		modelCheckScore(t, "P8", m.Score(v.Posts["P8"]), -1.210184, 0.229668, 23, []string{"prof:strong", "spam:caps", "key:B"})
	})
}

func TestModelColdStart(t *testing.T) {
	vectors := modelReadVectors(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	t.Run("zero votes and zero value", func(t *testing.T) {
		check := func(m Model) {
			modelCheckFloat(t, "BaseRate", m.BaseRate(), 0.5)
			if m.Active() || len(m.Weights()) != 0 {
				t.Fatal("zero model should be inactive and have no weights")
			}
			modelCheckScore(t, "P1", m.Score(vectors["history"].Posts["P1"]), 0, 0.5, 0, nil)
		}
		check(Train(nil, now, nil))
		check(Model{})
	})
	t.Run("one vote", func(t *testing.T) {
		c := vectors["cold1"]
		m := Train(c.Votes, now, nil)
		modelCheckFloat(t, "BaseRate", m.BaseRate(), 2.0/3)
		if m.Active() {
			t.Fatal("one vote activated model")
		}
		modelCheckFloat(t, "lang:en W", modelFindWeight(t, m.Weights(), "lang:en").W, 0.318454)
		w := m.Weights()
		modelCheckWeight(t, w, 0, "code:block", 1, 0, 0.318454)
		modelCheckWeight(t, w, 1, "key:A", 1, 0, 0.318454)
		modelCheckWeight(t, w, 2, "lang:en", 1, 0, 0.318454)
		modelCheckWeight(t, w, 3, "len:medium", 1, 0, 0.318454)
		modelCheckWeight(t, w, 4, "thread:reply", 1, 0, 0.318454)
		modelCheckWeight(t, w, 5, "topic:tech", 1, 0, 0.318454)
		modelCheckScoreNumbers(t, m.Score(c.Posts["P1"]), 0.796134, 0.689147, 5)
	})
	t.Run("five votes", func(t *testing.T) {
		c := vectors["cold5"]
		m := Train(c.Votes, now, nil)
		modelCheckFloat(t, "BaseRate", m.BaseRate(), 0.571429)
		if m.Active() {
			t.Fatal("five votes activated model")
		}
		w := m.Weights()
		modelCheckFloat(t, "topic:tech W", modelFindWeight(t, w, "topic:tech").W, 0.628609)
		modelCheckFloat(t, "code:block W", modelFindWeight(t, w, "code:block").W, 0.362905)
		modelCheckFloat(t, "key:A W", modelFindWeight(t, w, "key:A").W, 0.362905)
		modelCheckFloat(t, "thread:reply W", modelFindWeight(t, w, "thread:reply").W, -0.096627)
		modelCheckFloat(t, "lang:en W", modelFindWeight(t, w, "lang:en").W, 0.065139)
		modelCheckFloat(t, "len:short W", modelFindWeight(t, w, "len:short").W, 0.169076)
		modelCheckFloat(t, "prof:strong W", modelFindWeight(t, w, "prof:strong").W, -0.459532)
		modelCheckScoreNumbers(t, m.Score(c.Posts["P1"]), 0.746004, 0.678307, 14)
	})
}

func TestModelDecayAndActivation(t *testing.T) {
	vectors := modelReadVectors(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	h := vectors["history"]
	t.Run("90 day decay", func(t *testing.T) {
		votes := append([]Vote(nil), h.Votes...)
		d2 := slices.IndexFunc(votes, func(vote Vote) bool { return vote.PostID == "D2" })
		if d2 < 0 {
			t.Fatal("history has no D2 vote")
		}
		votes[d2].At = now.Add(-90 * 24 * time.Hour)
		m := Train(votes, now, nil)
		modelCheckFloat(t, "BaseRate", m.BaseRate(), 0.518519)
		modelCheckFloat(t, "prof:strong Likes", modelFindWeight(t, m.Weights(), "prof:strong").Likes, 0)
		modelCheckFloat(t, "prof:strong Dislikes", modelFindWeight(t, m.Weights(), "prof:strong").Dislikes, 1.5)
		modelCheckFloat(t, "prof:strong W", modelFindWeight(t, m.Weights(), "prof:strong").W, -0.575965)
		modelCheckFloat(t, "thread:reply W", modelFindWeight(t, m.Weights(), "thread:reply").W, -0.156944)
		modelCheckFloat(t, "len:short W", modelFindWeight(t, m.Weights(), "len:short").W, 0.038733)
		modelCheckFloat(t, "lang:en W", modelFindWeight(t, m.Weights(), "lang:en").W, 0.009573)
		modelCheckScoreNumbers(t, m.Score(h.Posts["P2"]), -0.342302, 0.415251, 25)
	})
	check := func(t *testing.T, key string, wantActive bool) {
		t.Helper()
		if got := Train(vectors[key].Votes, now, nil).Active(); got != wantActive {
			t.Errorf("Active() = %t; want %t", got, wantActive)
		}
	}
	t.Run("activation 9 likes 1 dislike", func(t *testing.T) { check(t, "activation_9_1", false) })
	t.Run("activation 2 likes 8 dislikes", func(t *testing.T) { check(t, "activation_2_8", true) })
	t.Run("activation 5 likes 4 dislikes", func(t *testing.T) { check(t, "activation_5_4", false) })
	t.Run("activation decayed total 9.5", func(t *testing.T) { check(t, "activation_decay", false) })
	t.Run("activation future vote counts as one", func(t *testing.T) {
		check(t, "activation_future", true)
		future := Train(vectors["activation_future"].Votes, now, nil)
		modelCheckFloat(t, "future like count", modelFindWeight(t, future.Weights(), "a").Likes, 5)
	})
}

func TestModelMutedAndClamp(t *testing.T) {
	vectors := modelReadVectors(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	h := vectors["history"]
	t.Run("muted indicator reduces support", func(t *testing.T) {
		m := Train(h.Votes, now, map[string]bool{"topic:tech": true})
		w := modelFindWeight(t, m.Weights(), "topic:tech")
		modelCheckFloat(t, "topic:tech likes", w.Likes, 3)
		modelCheckFloat(t, "topic:tech dislikes", w.Dislikes, 0)
		modelCheckFloat(t, "topic:tech W", w.W, 0)
		if !w.Muted {
			t.Fatal("topic:tech not marked muted")
		}
		modelCheckScore(t, "P1", m.Score(h.Posts["P1"]), 0.784308, 0.686608, 30, []string{"code:block", "key:A", "thread:deep"})
	})
	t.Run("unseen muted indicator changes no scores", func(t *testing.T) {
		m := Train(h.Votes, now, map[string]bool{"never:seen": true})
		unseen := modelFindWeight(t, m.Weights(), "never:seen")
		if !unseen.Muted || unseen.W != 0 || unseen.Likes != 0 || unseen.Dislikes != 0 {
			t.Fatalf("unexpected unseen muted row: %+v", unseen)
		}
		if got := len(m.Weights()); got != 21 {
			t.Errorf("Weights() has %d rows; want 21", got)
		}
		got := slices.DeleteFunc(m.Weights(), func(weight Weight) bool { return weight.Indicator == "never:seen" })
		base := Train(h.Votes, now, nil)
		if !reflect.DeepEqual(got, base.Weights()) {
			t.Error("unseen mute changed existing weight rows")
		}
		if !reflect.DeepEqual(m.Score(h.Posts["P1"]), base.Score(h.Posts["P1"])) {
			t.Error("unseen mute changed score")
		}
	})
	t.Run("clamp", func(t *testing.T) {
		m := Train(vectors["clamp"].Votes, now, nil)
		modelCheckFloat(t, "BaseRate", m.BaseRate(), 0.5)
		modelCheckFloat(t, "x W", modelFindWeight(t, m.Weights(), "x").W, 3)
		modelCheckFloat(t, "y W", modelFindWeight(t, m.Weights(), "y").W, -3)
		modelCheckScore(t, "x", m.Score([]string{"x"}), 1.5, 0.817574, 50, []string{"x"})
	})
}

func TestModelOrderAndDuplicateVotes(t *testing.T) {
	vectors := modelReadVectors(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	h := vectors["history"]
	t.Run("input order and immutability", func(t *testing.T) {
		original := append([]Vote(nil), h.Votes...)
		reversed := append([]Vote(nil), h.Votes...)
		slices.Reverse(reversed)
		first := Train(h.Votes, now, nil)
		second := Train(reversed, now, nil)
		if !reflect.DeepEqual(first.Weights(), second.Weights()) || !reflect.DeepEqual(first.Score(h.Posts["P1"]), second.Score(h.Posts["P1"])) {
			t.Fatal("reversed votes changed model results")
		}
		if !reflect.DeepEqual(h.Votes, original) {
			t.Fatal("Train modified input votes")
		}
	})
	t.Run("equal time dislike wins", func(t *testing.T) {
		votes := append(append([]Vote(nil), h.Votes...), Vote{PostID: "H6", Like: false, At: now, Indicators: []string{"lang:en", "len:short", "media:image"}})
		m := Train(votes, now, nil)
		modelCheckFloat(t, "BaseRate", m.BaseRate(), 0.428571)
	})
	t.Run("older duplicate loses", func(t *testing.T) {
		votes := append(append([]Vote(nil), h.Votes...), Vote{PostID: "H6", Like: false, At: now.Add(-time.Hour), Indicators: []string{"changed"}})
		m := Train(votes, now, nil)
		base := Train(h.Votes, now, nil)
		if !reflect.DeepEqual(m.Weights(), base.Weights()) {
			t.Fatal("older duplicate replaced newer vote")
		}
	})
}
