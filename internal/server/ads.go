package server

import (
	"net/http"
	"slices"
	"sort"
	"strings"
)

// demoAd is a sponsored post and the things it is about: post topics, or the
// IDs of single posts, that make it relevant to a reader who liked them.
type demoAd struct {
	post      post
	interests []string
}

// Ads live in their own section and are never mixed into a feed by relevance.
// Three of them are also ordinary sponsored posts in the demo feed.
var demoAds = buildDemoAds()

func buildDemoAds() []demoAd {
	ad := func(id, color, text string, interests ...string) demoAd {
		return demoAd{post: post{ID: id, Kind: "text", Color: color, Time: "Ad", Topic: "Sponsored", Text: text}, interests: interests}
	}
	ads := []demoAd{
		ad("ad-coffee", "peach", "Meet your new morning ritual. Try a locally roasted coffee subscription with free delivery on your first order.", "Learning", "recipe", "journal"),
		ad("ad-bike", "mint", "Make the commute yours. Explore refurbished city bikes and accessories at our spring sale.", "Travel", "Fitness", "Sports"),
		ad("ad-app", "peach", "Organize your week with a simple planner. Try the free version and see whether it fits your routine.", "Personal finance", "Design", "journal"),
		ad("ad-telescope", "blue", "See Saturn's rings from your balcony. Beginner tabletop telescopes, shipped assembled.", "Science", "Space"),
		ad("ad-seeds", "mint", "Heirloom tomato and basil seed kits for next spring, grown and packed by a family farm.", "Gardening", "From the garden", "maya"),
		ad("ad-sketchbook", "lilac", "Pocket sketchbooks with paper that takes ink without bleeding through.", "Art", "Design"),
		ad("ad-headphones", "blue", "Open-back headphones tuned for jazz and long train rides.", "Music", "Travel"),
		ad("ad-shoes", "peach", "Trail shoes for your first comfortable five kilometers. Free returns for 60 days.", "Fitness", "Sports"),
		ad("ad-cookbook", "mint", "Forty weeknight dinners from one tin of chickpeas. The paperback is out now.", "recipe", "Learning", "Gardening"),
		ad("ad-stand", "lilac", "A walnut laptop stand that keeps a home lab cool and quiet.", "Technology", "DIY", "workshop"),
		ad("ad-petbed", "peach", "Two beds for one price: one for the dog and one for the cat who refuses to share.", "Animals"),
		ad("ad-strap", "blue", "Hand-stitched camera straps for long exposure nights.", "Photography"),
	}
	for i := range ads {
		ads[i].post.Receipt = mockReceipt(ads[i].post.ID)
		ads[i].post.ReplyCount = countReplies(demoReplies(ads[i].post.ID))
	}
	return ads
}

// adsFor returns the ads this reader has not disliked, most relevant first.
// Relevance is counted here, from the reader's own likes and dislikes; nothing
// about the reader is sent anywhere. Ads with the same count keep their order.
func adsFor(r *http.Request) []post {
	votes := feedbackEntries(r)
	chosen := make([]post, 0, len(demoAds))
	scores := make(map[string]int, len(demoAds))
	for _, ad := range demoAds {
		score, reasons, disliked := 0, []string(nil), false
		for _, vote := range votes {
			if vote.ID == ad.post.ID {
				disliked = disliked || vote.Vote == "down"
				continue
			}
			voted, known := postByID(vote.ID)
			if !known || !(slices.Contains(ad.interests, voted.Topic) && voted.Topic != "" || slices.Contains(ad.interests, voted.ID)) {
				continue
			}
			if vote.Vote == "down" {
				score--
				continue
			}
			score++
			if voted.Topic != "" && !slices.Contains(reasons, voted.Topic) && len(reasons) < 2 {
				reasons = append(reasons, voted.Topic)
			}
		}
		if disliked {
			continue
		}
		shown := ad.post
		if score > 0 && len(reasons) > 0 {
			shown.Why = "Because you liked posts about " + strings.Join(reasons, " and ")
		} else if score > 0 {
			shown.Why = "Because of a post you liked"
		}
		scores[shown.ID] = score
		chosen = append(chosen, shown)
	}
	sort.SliceStable(chosen, func(i, j int) bool { return scores[chosen[i].ID] > scores[chosen[j].ID] })
	return chosen
}

// adsOutsideFeed are the ads that appear only in the ads section.
var adsOutsideFeed = func() []post {
	inFeed := make(map[string]bool, len(staticDemoPosts))
	for _, p := range staticDemoPosts {
		inFeed[p.ID] = true
	}
	var outside []post
	for _, ad := range demoAds {
		if !inFeed[ad.post.ID] {
			outside = append(outside, ad.post)
		}
	}
	return outside
}()

func likedSet(r *http.Request) map[string]bool {
	liked := make(map[string]bool)
	for _, vote := range feedbackEntries(r) {
		if vote.Vote == "up" {
			liked[vote.ID] = true
		}
	}
	return liked
}
