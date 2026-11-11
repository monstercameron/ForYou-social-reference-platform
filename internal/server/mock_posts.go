package server

// extraPosts mixes ordinary content with material a real client may need to handle.
func extraPosts() []post {
	return []post{
		{ID: "tech-video", Kind: "video", Art: "tech-video", Color: "blue", Time: "22 min ago", Topic: "Technology", Text: "A quick video test from my home lab: comparing a tiny server's power use before and after an upgrade."},
		{ID: "ad-coffee", Kind: "text", Color: "peach", Time: "26 min ago", Topic: "Sponsored", Text: "Meet your new morning ritual. Try a locally roasted coffee subscription with free delivery on your first order."},
		{ID: "art-studio", Kind: "text", Color: "mint", Time: "31 min ago", Topic: "Art", Text: "Spent the afternoon making tiny ink drawings of the plants growing through cracks in the sidewalk."},
		{ID: "adult-1", Kind: "text", Color: "lilac", Time: "35 min ago", Topic: "Adult content", Text: "18+ creator: new adult photo set available on my page. Adults only."},
		{ID: "science", Kind: "text", Color: "blue", Time: "39 min ago", Topic: "Science", Text: "Tonight's sky is clear enough to spot Jupiter. Binoculars can reveal its brightest moons."},
		{ID: "violence", Kind: "text", Color: "peach", Time: "44 min ago", Topic: "Violence", Text: "A disturbing clip shows a street fight outside the station. Please check on anyone affected and avoid sharing identifying details."},
		{ID: "garden", Kind: "text", Color: "mint", Time: "51 min ago", Topic: "Gardening", Text: "The tomatoes finally ripened. Next year I'm planting twice as many basil plants alongside them."},
		{ID: "harassment", Kind: "text", Color: "lilac", Time: "55 min ago", Topic: "Harassment", Text: "Nobody likes you. Stop posting here. You are embarrassing yourself and everyone is laughing at you."},
		{ID: "music", Kind: "text", Color: "blue", Time: "1 hour ago", Topic: "Music", Text: "What album do you always return to on long train rides? Looking for something new for the weekend."},
		{ID: "ad-bike", Kind: "text", Color: "mint", Time: "1 hour ago", Topic: "Sponsored", Text: "Make the commute yours. Explore refurbished city bikes and accessories at our spring sale."},
		{ID: "travel", Kind: "text", Color: "peach", Time: "2 hours ago", Topic: "Travel", Text: "Found a quiet footpath along the harbor, far from the crowds. The ferry at sunset was the highlight."},
		{ID: "gaming", Kind: "text", Color: "lilac", Time: "2 hours ago", Topic: "Gaming", Text: "Our co-op team finally solved the puzzle after an hour of confidently following the wrong map."},
		{ID: "adult-2", Kind: "text", Color: "blue", Time: "3 hours ago", Topic: "Adult content", Text: "Adults only: a creator shares a link to an explicit video. Viewer discretion advised."},
		{ID: "open-source", Kind: "text", Color: "mint", Time: "3 hours ago", Topic: "Technology", Text: "Released a small open-source tool for checking broken links in documentation. Feedback and issue reports welcome."},
		{ID: "pets", Kind: "text", Color: "peach", Time: "3 hours ago", Topic: "Animals", Text: "The dog has decided that the new bed belongs to the cat. The cat disagrees."},
		{ID: "scam", Kind: "text", Color: "lilac", Time: "4 hours ago", Topic: "Suspicious promotion", Text: "Congratulations! You have been selected for a mystery prize. Send your wallet details now to claim it!"},
		{ID: "sports", Kind: "text", Color: "blue", Time: "4 hours ago", Topic: "Sports", Text: "A last-minute goal turned the neighborhood match around. Brilliant teamwork from both sides."},
		{ID: "weather-alert", Kind: "text", Color: "mint", Time: "4 hours ago", Topic: "Weather", Text: "Heavy rain is expected this evening. Check local alerts before traveling and bring an umbrella."},
		{ID: "ad-app", Kind: "text", Color: "peach", Time: "5 hours ago", Topic: "Sponsored", Text: "Organize your week with a simple planner. Try the free version and see whether it fits your routine."},
		{ID: "bullying", Kind: "text", Color: "lilac", Time: "5 hours ago", Topic: "Harassment", Text: "You are pathetic and should quit. Nobody wants to hear your opinion."},
		{ID: "photography", Kind: "text", Color: "blue", Time: "6 hours ago", Topic: "Photography", Text: "Experimenting with long exposures after dark: wet pavement and passing buses make wonderful streaks of color."},
		{ID: "finance", Kind: "text", Color: "mint", Time: "6 hours ago", Topic: "Personal finance", Text: "A small emergency fund can make an unexpected repair less stressful. What budgeting habit helped you most?"},
		{ID: "news", Kind: "text", Color: "peach", Time: "7 hours ago", Topic: "Local news", Text: "The library will stay open later on Thursdays after residents asked for more evening hours."},
		{ID: "fitness", Kind: "text", Color: "lilac", Time: "7 hours ago", Topic: "Fitness", Text: "First comfortable five-kilometer run in months. Slow progress is still progress."},
		{ID: "threat", Kind: "text", Color: "blue", Time: "8 hours ago", Topic: "Threatening content", Text: "I'm furious about what happened and want to hurt someone. This post is an example of a threat that needs attention."},
		{ID: "diy", Kind: "text", Color: "mint", Time: "8 hours ago", Topic: "DIY", Text: "Built a narrow shelf from leftover wood for the kitchen. The hardest part was getting the wall anchors straight."},
		{ID: "history", Kind: "text", Color: "peach", Time: "9 hours ago", Topic: "History", Text: "An old map showed a stream running under our street. Does anyone know when it was covered over?"},
		{ID: "misinformation", Kind: "text", Color: "lilac", Time: "9 hours ago", Topic: "Unverified claim", Text: "Someone claims a miracle device can charge a phone forever without power. No evidence was provided."},
		{ID: "design", Kind: "text", Color: "blue", Time: "10 hours ago", Topic: "Design", Text: "A good empty state should help someone take the next step, not just announce that nothing is here."},
		{ID: "education", Kind: "text", Color: "mint", Time: "10 hours ago", Topic: "Learning", Text: "Today I learned why bread dough gets stronger as you knead it. Food science makes baking more fun."},
		{ID: "community", Kind: "text", Color: "peach", Time: "11 hours ago", Topic: "Community", Text: "The neighborhood repair cafe meets Saturday. Bring a broken lamp, a bicycle, or a willingness to help."},
		{ID: "space", Kind: "text", Color: "lilac", Time: "11 hours ago", Topic: "Space", Text: "A new telescope image can make the familiar night sky feel enormous all over again."},
		{ID: "spam-2", Kind: "text", Color: "blue", Time: "12 hours ago", Topic: "Spam", Text: "FOLLOW ME!!! FOLLOW ME!!! CLICK NOW!!! FREE FREE FREE!!! LIMITED TIME!!! CLICK NOW!!!"},
		{ID: "poetry", Kind: "text", Color: "mint", Time: "12 hours ago", Topic: "Poetry", Text: "Rain taps the window / the kettle answers softly / morning starts again."},
	}
}

func buildDemoPosts() []post {
	posts := []post{
		{ID: "maya", Kind: "text", ReplyCount: 4, Name: "Maya Chen", Handle: "maya.c", Initials: "MC", Color: "lilac", Time: "12 min ago", Topic: "A little thought", Text: "What if the best corners of the internet felt a little more like a community garden? Open gates, room to grow, and everyone tending to something different."},
		{ID: "elliot", Kind: "video", ReplyCount: 3, Name: "Elliot Brooks", Handle: "elliot.b", Initials: "EB", Color: "peach", Time: "48 min ago", Topic: "From the garden", Text: "A closer look at the small things growing around us.", Art: "video"},
		{ID: "noor", Kind: "text", ReplyCount: 2, Name: "Noor Williams", Handle: "noor.w", Initials: "NW", Color: "mint", Time: "2 hours ago", Topic: "In conversation", Text: "The most interesting spaces are the ones that give people the tools to make them their own.", IsReply: true},
		{ID: "sofia", Kind: "images", ReplyCount: 3, Name: "Sofia Park", Handle: "sofia.p", Initials: "SP", Color: "blue", Time: "4 hours ago", Topic: "A little thought", Text: "A few quiet moments from the mountains.", Art: "gallery"},
		{ID: "journal", Kind: "text", Color: "lilac", Time: "5 hours ago", Text: "I started keeping a small notebook beside my desk, not for big plans but for the tiny observations that usually disappear by the end of the day. The light on the wall after lunch. A sentence a friend said that made me laugh. The way a familiar street sounds after rain.\n\nAfter a few weeks, the notebook became a map of things I might otherwise have missed. None of the entries are important on their own, but together they make an ordinary week feel wonderfully specific. If you try it, don't worry about writing well. Just notice something and put it down."},
		{ID: "noise", Kind: "text", Color: "peach", Time: "6 hours ago", Text: "LIMITED OFFER! LIMITED OFFER! AMAZING DEALS TODAY! AMAZING DEALS TODAY! CLICK NOW! CLICK NOW! LIMITED OFFER! AMAZING DEALS TODAY! DON'T MISS OUT! DON'T MISS OUT! LIMITED OFFER! AMAZING DEALS TODAY! CLICK NOW! CLICK NOW!"},
		{ID: "recipe", Kind: "text", Color: "mint", Time: "7 hours ago", Text: "Dinner idea: roast tomatoes, garlic and chickpeas with olive oil, then add lemon and herbs. Serve with warm bread and whatever greens you have. Simple, inexpensive, and even better the next day."},
		{ID: "weather", Kind: "text", Color: "blue", Time: "8 hours ago", Text: "First snow of the season. ❄️"},
		{ID: "books", Kind: "text", Color: "lilac", Time: "9 hours ago", Text: "What book changed the way you think about a familiar place? I'm looking for stories that make a neighborhood, a train station, or a coastline feel like a character in its own right. Fiction, essays, and poetry are all welcome."},
		{ID: "workshop", Kind: "text", Color: "peach", Time: "10 hours ago", Text: "Small workshop update: the prototype finally runs quietly enough to use on a desk. The first version buzzed like an angry bee, and the second rattled whenever it warmed up. This one isn't perfect, but replacing the mount made a surprising difference. Next up: documenting the measurements and sharing what didn't work, so someone else doesn't have to repeat the same mistakes."},
	}
	posts = append(posts[:2:2], append(extraPosts(), posts[2:]...)...)
	for i := range posts {
		posts[i].Receipt = mockReceipt(posts[i].ID)
		posts[i].ReplyCount = len(demoReplies(posts[i].ID))
	}
	return posts
}

// Demo records are immutable, so their identifiers only need to be computed once.
var staticDemoPosts = buildDemoPosts()
