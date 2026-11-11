package server

import "crypto/sha256"

func demoReplies(id string) []reply {
	switch id {
	case "maya":
		return []reply{
			{Name: "Noor Williams", Initials: "NW", Color: "mint", Time: "9 min ago", Text: "I love this. A little more tending and a lot less noise.", Children: []reply{
				{Name: "Maya Chen", Initials: "MC", Color: "lilac", Time: "8 min ago", Text: "Exactly! It should feel welcoming to linger.", Children: []reply{
					{Name: "Noor Williams", Initials: "NW", Color: "mint", Time: "6 min ago", Text: "And to come back whenever you feel like it."},
				}},
			}},
			{Name: "Elliot Brooks", Initials: "EB", Color: "peach", Time: "7 min ago", Text: "And room for things to grow at their own pace."},
			{Name: "Sofia Park", Initials: "SP", Color: "blue", Time: "5 min ago", Text: "The open gates part is my favorite."},
			{Name: "Avery Stone", Initials: "AS", Color: "lilac", Time: "2 min ago", Text: "More community gardens, online and off."},
		}
	case "elliot":
		return []reply{
			{Name: "Maya Chen", Initials: "MC", Color: "lilac", Time: "40 min ago", Text: "These colors feel like a deep breath.", Children: []reply{
				{Name: "Elliot Brooks", Initials: "EB", Color: "peach", Time: "38 min ago", Text: "It was one of those mornings I wanted to keep."},
			}},
			{Name: "Noor Williams", Initials: "NW", Color: "mint", Time: "32 min ago", Text: "Such a peaceful morning."},
			{Name: "Sofia Park", Initials: "SP", Color: "blue", Time: "20 min ago", Text: "I needed this today. Thanks for sharing."},
		}
	case "noor":
		return []reply{
			{Name: "Maya Chen", Initials: "MC", Color: "lilac", Time: "1 hour ago", Text: "Yes! That freedom makes all the difference."},
			{Name: "Elliot Brooks", Initials: "EB", Color: "peach", Time: "45 min ago", Text: "Small communities bring so much character."},
		}
	case "sofia":
		return []reply{
			{Name: "Noor Williams", Initials: "NW", Color: "mint", Time: "3 hours ago", Text: "The messy middle is where I learn the most."},
			{Name: "Maya Chen", Initials: "MC", Color: "lilac", Time: "2 hours ago", Text: "A lovely reminder to keep showing up."},
			{Name: "Elliot Brooks", Initials: "EB", Color: "peach", Time: "1 hour ago", Text: "Sharing progress makes the whole process feel less lonely."},
		}
	default:
		return variedDemoReplies(id)
	}
}

// Mock reply groups are immutable; rendering copies them before adding receipts.
var demoReplyGroups = [][]reply{
	{
		{Color: "mint", Time: "8 min ago", Text: "Thanks for sharing this. I hadn't thought of it that way.", Children: []reply{{Color: "blue", Time: "6 min ago", Text: "Same here. The small details make a difference."}}},
		{Color: "peach", Time: "5 min ago", Text: "This made my day a little better."},
		{Color: "lilac", Time: "2 min ago", Text: "I disagree, but I appreciate the perspective."},
	},
	{
		{Color: "blue", Time: "18 min ago", Text: "What a lovely photo. That outfit is gorgeous and the mood is really seductive."},
		{Color: "lilac", Time: "12 min ago", Text: "Flirting aside, the lighting is fantastic."},
		{Color: "mint", Time: "4 min ago", Text: "Adults only, please respect everyone's boundaries."},
	},
	{
		{Color: "peach", Time: "25 min ago", Text: "You've won a mystery prize! Send your wallet recovery phrase to claim it."},
		{Color: "blue", Time: "19 min ago", Text: "That's a scam. Never share your recovery phrase."},
		{Color: "lilac", Time: "9 min ago", Text: "FREE DEAL FREE DEAL FOLLOW ME FOLLOW ME!!!"},
	},
	{
		{Color: "lilac", Time: "15 min ago", Text: "This is a stupid take. What the hell were you thinking?"},
		{Color: "mint", Time: "11 min ago", Text: "You can disagree without being an asshole."},
		{Color: "peach", Time: "4 min ago", Text: "Yeah, this argument is bullshit, but the personal insults aren't helping."},
	},
	{
		{Color: "mint", Time: "12 min ago", Text: "Can you share more about how you made this?", Children: []reply{{Color: "peach", Time: "7 min ago", Text: "I'd like to know too.", Children: []reply{{Color: "blue", Time: "3 min ago", Text: "Following along for the details."}}}}},
		{Color: "blue", Time: "6 min ago", Text: "Great work. This is the kind of thing I like seeing here."},
		{Color: "lilac", Time: "1 min ago", Text: "I'm not convinced, but it's an interesting idea."},
	},
}

// variedDemoReplies supplies deliberately mixed discussion for the mock feed.
// These fixtures are not actual replies from network users.
func variedDemoReplies(id string) []reply {
	switch id {
	case "adult-1", "adult-2":
		return demoReplyGroups[1]
	case "scam":
		return demoReplyGroups[2]
	case "spam-2", "noise":
		return []reply{
			{Color: "peach", Time: "12 min ago", Text: "FREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREE"},
			{Color: "blue", Time: "8 min ago", Text: "Please stop posting the same advertisement everywhere."},
			{Color: "mint", Time: "3 min ago", Text: "This looks like spam. Don't send anyone your account details."},
		}
	case "harassment", "bullying":
		return demoReplyGroups[3]
	case "tech-video", "open-source":
		return demoReplyGroups[4]
	case "science", "space":
		return []reply{
			{Color: "blue", Time: "11 min ago", Text: "I spotted Jupiter too! The moons were surprisingly clear through binoculars."},
			{Color: "mint", Time: "8 min ago", Text: "Clear skies here tonight. Thanks for the reminder."},
			{Color: "lilac", Time: "3 min ago", Text: "Does anyone have a recommendation for a beginner telescope?"},
		}
	case "garden":
		return []reply{
			{Color: "mint", Time: "20 min ago", Text: "Tomatoes and basil are a great combination. Mine finally ripened too."},
			{Color: "peach", Time: "12 min ago", Text: "Try adding a little compost before planting next spring."},
			{Color: "blue", Time: "4 min ago", Text: "Damn, now I want a garden instead of this tiny balcony."},
		}
	case "music":
		return []reply{
			{Color: "lilac", Time: "21 min ago", Text: "I keep coming back to old jazz records on the train."},
			{Color: "blue", Time: "10 min ago", Text: "A whole album beats a shuffled playlist for a long ride."},
			{Color: "mint", Time: "2 min ago", Text: "My recommendation is anything you can listen to without skipping."},
		}
	case "recipe":
		return []reply{
			{Color: "peach", Time: "18 min ago", Text: "Made something similar with smoked paprika. It was delicious."},
			{Color: "mint", Time: "12 min ago", Text: "Chickpeas are so good for cheap weeknight dinners."},
			{Color: "blue", Time: "4 min ago", Text: "How long do you roast the tomatoes?"},
		}
	case "pets":
		return []reply{
			{Color: "mint", Time: "16 min ago", Text: "The cat will win this negotiation, obviously."},
			{Color: "blue", Time: "9 min ago", Text: "We bought two beds and somehow they both want the same one."},
			{Color: "peach", Time: "2 min ago", Text: "Please tell me they eventually shared it."},
		}
	case "weather-alert", "violence":
		return []reply{
			{Color: "blue", Time: "12 min ago", Text: "Thanks for the heads-up. Please check reliable local sources for updates."},
			{Color: "mint", Time: "7 min ago", Text: "Hope everyone nearby is safe."},
			{Color: "lilac", Time: "3 min ago", Text: "Sharing unverified details can make a bad situation worse."},
		}
	}
	sum := sha256.Sum256([]byte(id))
	return demoReplyGroups[int(sum[0])%2*4]
}
