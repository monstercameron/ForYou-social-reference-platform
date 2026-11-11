package server

import "crypto/sha256"

func say(color, age, text string, children ...reply) reply {
	return reply{Color: color, Time: age, Text: text, Children: children}
}

// demoReplies returns the read-only fixture thread of a post. Two posts stay
// without replies on purpose; a post without a thread of its own gets an everyday one.
func demoReplies(id string) []reply {
	if id == "weather" || id == "poetry" {
		return nil
	}
	if replies, ok := demoThreads[id]; ok {
		return replies
	}
	sum := sha256.Sum256([]byte(id))
	return everydayThreads[int(sum[0])%len(everydayThreads)]
}

func countReplies(replies []reply) int {
	total := len(replies)
	for _, r := range replies {
		total += countReplies(r.Children)
	}
	return total
}

var everydayThreads = [][]reply{
	{
		say("mint", "8 min ago", "Thanks for sharing this. I hadn't thought of it that way.",
			say("blue", "6 min ago", "Same here. The small details make a difference.")),
		say("peach", "5 min ago", "This made my day a little better."),
		say("lilac", "2 min ago", "I disagree, but I appreciate the perspective."),
	},
	{
		say("mint", "12 min ago", "Can you share more about how you made this?",
			say("peach", "7 min ago", "I'd like to know too.",
				say("blue", "3 min ago", "Following along for the details."))),
		say("blue", "6 min ago", "Great work. This is the kind of thing I like seeing here."),
		say("lilac", "1 min ago", "I'm not convinced, but it's an interesting idea."),
	},
	{
		say("lilac", "14 min ago", "Who asked? Nobody gives a shit.",
			say("mint", "11 min ago", "Be kind or scroll on.")),
		say("blue", "9 min ago", "Honestly a nice post. Ignore the idiot above."),
		say("peach", "3 min ago", "Saving this one for later."),
	},
	{
		say("blue", "25 min ago", "Where was this? I'd like to see it for myself.",
			say("mint", "21 min ago", "Not far from the old tram depot, if I recognise it.",
				say("blue", "17 min ago", "Thanks. Going on Sunday."))),
		say("peach", "13 min ago", "More of this, please."),
		say("lilac", "4 min ago", "Damn, that's good."),
	},
}

var flirtThread = []reply{
	say("blue", "18 min ago", "What a lovely photo. That outfit is gorgeous and the mood is really seductive."),
	say("lilac", "12 min ago", "Flirting aside, the lighting is fantastic."),
	say("mint", "4 min ago", "Adults only, please respect everyone's boundaries."),
}

var prizeScamThread = []reply{
	say("peach", "25 min ago", "You've won a mystery prize! Send your wallet recovery phrase to claim it."),
	say("blue", "19 min ago", "That's a scam. Never share your recovery phrase."),
	say("lilac", "9 min ago", "FREE DEAL FREE DEAL FOLLOW ME FOLLOW ME!!!"),
}

var spamThread = []reply{
	say("peach", "12 min ago", "FREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREEFREE"),
	say("blue", "8 min ago", "Please stop posting the same advertisement everywhere."),
	say("mint", "3 min ago", "This looks like spam. Don't send anyone your account details."),
}

var nightSkyThread = []reply{
	say("blue", "11 min ago", "I spotted Jupiter too! The moons were surprisingly clear through binoculars."),
	say("mint", "8 min ago", "Clear skies here tonight. Thanks for the reminder."),
	say("lilac", "3 min ago", "Does anyone have a recommendation for a beginner telescope?",
		say("blue", "2 min ago", "A 130 mm tabletop reflector. Cheap, and it shows Saturn's rings.")),
}

var rainThread = []reply{
	say("blue", "12 min ago", "Thanks for the heads-up. Please check reliable local sources for updates."),
	say("mint", "7 min ago", "Hope everyone nearby is safe."),
	say("lilac", "3 min ago", "The river path floods first. Take the upper road if you're cycling."),
}

var demoThreads = map[string][]reply{
	"maya": {
		say("mint", "9 min ago", "I love this. A little more tending and a lot less noise.",
			say("lilac", "8 min ago", "Exactly! It should feel welcoming to linger.",
				say("mint", "6 min ago", "And to come back whenever you feel like it."))),
		say("peach", "7 min ago", "And room for things to grow at their own pace."),
		say("blue", "5 min ago", "The open gates part is my favorite."),
		say("lilac", "2 min ago", "More community gardens, online and off."),
	},
	"elliot": {
		say("lilac", "40 min ago", "These colors feel like a deep breath.",
			say("peach", "38 min ago", "It was one of those mornings I wanted to keep.")),
		say("mint", "32 min ago", "Such a peaceful morning."),
		say("blue", "20 min ago", "I needed this today. Thanks for sharing."),
	},
	"noor": {
		say("lilac", "1 hour ago", "Yes! That freedom makes all the difference."),
		say("peach", "45 min ago", "Small communities bring so much character."),
	},
	"sofia": {
		say("mint", "3 hours ago", "The messy middle is where I learn the most."),
		say("lilac", "2 hours ago", "A lovely reminder to keep showing up."),
		say("peach", "1 hour ago", "Sharing progress makes the whole process feel less lonely."),
	},
	"garden": {
		say("mint", "20 min ago", "Tomatoes and basil are a great combination. Mine finally ripened too."),
		say("peach", "12 min ago", "Try adding a little compost before planting next spring."),
		say("blue", "4 min ago", "Damn, now I want a garden instead of this tiny balcony."),
	},
	"music": {
		say("lilac", "21 min ago", "I keep coming back to old jazz records on the train."),
		say("blue", "10 min ago", "A whole album beats a shuffled playlist for a long ride."),
		say("mint", "2 min ago", "My recommendation is anything you can listen to without skipping."),
	},
	"recipe": {
		say("peach", "18 min ago", "Made something similar with smoked paprika. It was delicious."),
		say("mint", "12 min ago", "Chickpeas are so good for cheap weeknight dinners."),
		say("blue", "4 min ago", "How long do you roast the tomatoes?",
			say("mint", "2 min ago", "About 25 minutes at 220 °C, until the skins split.")),
	},
	"pets": {
		say("mint", "16 min ago", "The cat will win this negotiation, obviously."),
		say("blue", "9 min ago", "We bought two beds and somehow they both want the same one."),
		say("peach", "2 min ago", "Please tell me they eventually shared it."),
	},
	"adult-1":        flirtThread,
	"adult-2":        flirtThread,
	"scam":           prizeScamThread,
	"spam-2":         spamThread,
	"noise":          spamThread,
	"science":        nightSkyThread,
	"space":          nightSkyThread,
	"weather-alert":  rainThread,
	"open-source":    linkCheckerThread,
	"tech-video":     homeLabThread,
	"harassment":     pileOnThread,
	"bullying":       bullyingThread,
	"threat":         threatThread,
	"violence":       streetFightThread,
	"news":           libraryThread,
	"sports":         matchThread,
	"gaming":         puzzleThread,
	"finance":        budgetThread,
	"misinformation": miracleDeviceThread,
	"design":         emptyStateThread,
	"education":      doughThread,
	"diy":            shelfThread,
	"workshop":       prototypeThread,
}
