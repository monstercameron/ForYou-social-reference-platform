package main

import (
	"crypto/sha256"
	"strings"
)

type demoPost struct {
	id, text, kind string
}

var demoPosts = []demoPost{
	{"maya", "What if the best corners of the internet felt a little more like a community garden? Open gates, room to grow, and everyone tending to something different.", "text"},
	{"elliot", "A closer look at the small things growing around us.", "video"},
	{"tech-video", "A quick video test from my home lab: comparing a tiny server's power use before and after an upgrade.", "video"},
	{"ad-coffee", "Meet your new morning ritual. Try a locally roasted coffee subscription with free delivery on your first order.", "text"},
	{"art-studio", "Spent the afternoon making tiny ink drawings of the plants growing through cracks in the sidewalk.", "text"},
	{"adult-1", "18+ creator: new adult photo set available on my page. Adults only.", "text"},
	{"science", "Tonight's sky is clear enough to spot Jupiter. Binoculars can reveal its brightest moons.", "text"},
	{"violence", "A disturbing clip shows a street fight outside the station. Please check on anyone affected and avoid sharing identifying details.", "text"},
	{"garden", "The tomatoes finally ripened. Next year I'm planting twice as many basil plants alongside them.", "text"},
	{"harassment", "Nobody likes you. Stop posting here. You are embarrassing yourself and everyone is laughing at you.", "text"},
	{"music", "What album do you always return to on long train rides? Looking for something new for the weekend.", "text"},
	{"ad-bike", "Make the commute yours. Explore refurbished city bikes and accessories at our spring sale.", "text"},
	{"travel", "Found a quiet footpath along the harbor, far from the crowds. The ferry at sunset was the highlight.", "images"},
	{"gaming", "Our co-op team finally solved the puzzle after an hour of confidently following the wrong map.", "text"},
	{"adult-2", "Adults only: a creator shares a link to an explicit video. Viewer discretion advised.", "text"},
	{"open-source", "Released a small open-source tool for checking broken links in documentation. Feedback and issue reports welcome.", "text"},
	{"pets", "The dog has decided that the new bed belongs to the cat. The cat disagrees.", "text"},
	{"scam", "Congratulations! You have been selected for a mystery prize. Send your wallet details now to claim it!", "text"},
	{"sports", "A last-minute goal turned the neighborhood match around. Brilliant teamwork from both sides.", "text"},
	{"weather-alert", "Heavy rain is expected this evening. Check local alerts before traveling and bring an umbrella.", "text"},
	{"ad-app", "Organize your week with a simple planner. Try the free version and see whether it fits your routine.", "text"},
	{"bullying", "You are pathetic and should quit. Nobody wants to hear your opinion.", "text"},
	{"photography", "Experimenting with long exposures after dark: wet pavement and passing buses make wonderful streaks of color.", "images"},
	{"finance", "A small emergency fund can make an unexpected repair less stressful. What budgeting habit helped you most?", "text"},
	{"news", "The library will stay open later on Thursdays after residents asked for more evening hours.", "text"},
	{"fitness", "First comfortable five-kilometer run in months. Slow progress is still progress.", "text"},
	{"threat", "I'm furious about what happened and want to hurt someone. This post is an example of a threat that needs attention.", "text"},
	{"diy", "Built a narrow shelf from leftover wood for the kitchen. The hardest part was getting the wall anchors straight.", "text"},
	{"history", "An old map showed a stream running under our street. Does anyone know when it was covered over?", "text"},
	{"misinformation", "Someone claims a miracle device can charge a phone forever without power. No evidence was provided.", "text"},
	{"design", "A good empty state should help someone take the next step, not just announce that nothing is here.", "text"},
	{"education", "Today I learned why bread dough gets stronger as you knead it. Food science makes baking more fun.", "text"},
	{"community", "The neighborhood repair cafe meets Saturday. Bring a broken lamp, a bicycle, or a willingness to help.", "text"},
	{"space", "A new telescope image can make the familiar night sky feel enormous all over again.", "text"},
	{"spam-2", "FOLLOW ME!!! FOLLOW ME!!! CLICK NOW!!! FREE FREE FREE!!! LIMITED TIME!!! CLICK NOW!!!", "text"},
	{"poetry", "Rain taps the window / the kettle answers softly / morning starts again.", "text"},
	{"noor", "The most interesting spaces are the ones that give people the tools to make them their own.", "text"},
	{"sofia", "A few quiet moments from the mountains.", "images"},
	{"journal", "I started keeping a small notebook beside my desk, not for big plans but for the tiny observations that usually disappear by the end of the day. The light on the wall after lunch. A sentence a friend said that made me laugh. The way a familiar street sounds after rain.\n\nAfter a few weeks, the notebook became a map of things I might otherwise have missed. None of the entries are important on their own, but together they make an ordinary week feel wonderfully specific. If you try it, don't worry about writing well. Just notice something and put it down.", "text"},
	{"noise", "LIMITED OFFER! LIMITED OFFER! AMAZING DEALS TODAY! AMAZING DEALS TODAY! CLICK NOW! CLICK NOW! LIMITED OFFER! AMAZING DEALS TODAY! DON'T MISS OUT! DON'T MISS OUT! LIMITED OFFER! AMAZING DEALS TODAY! CLICK NOW! CLICK NOW!", "text"},
	{"recipe", "Dinner idea: roast tomatoes, garlic and chickpeas with olive oil, then add lemon and herbs. Serve with warm bread and whatever greens you have. Simple, inexpensive, and even better the next day.", "text"},
	{"weather", "First snow of the season. ❄️", "text"},
	{"books", "What book changed the way you think about a familiar place? I'm looking for stories that make a neighborhood, a train station, or a coastline feel like a character in its own right. Fiction, essays, and poetry are all welcome.", "text"},
	{"workshop", "Small workshop update: the prototype finally runs quietly enough to use on a desk. The first version buzzed like an angry bee, and the second rattled whenever it warmed up. This one isn't perfect, but replacing the mount made a surprising difference. Next up: documenting the measurements and sharing what didn't work, so someone else doesn't have to repeat the same mistakes.", "text"},
	{"ad-telescope", "See Saturn's rings from your balcony. Beginner tabletop telescopes, shipped assembled.", "text"},
	{"ad-seeds", "Heirloom tomato and basil seed kits for next spring, grown and packed by a family farm.", "text"},
	{"ad-sketchbook", "Pocket sketchbooks with paper that takes ink without bleeding through.", "text"},
	{"ad-headphones", "Open-back headphones tuned for jazz and long train rides.", "text"},
	{"ad-shoes", "Trail shoes for your first comfortable five kilometers. Free returns for 60 days.", "text"},
	{"ad-cookbook", "Forty weeknight dinners from one tin of chickpeas. The paperback is out now.", "text"},
	{"ad-stand", "A walnut laptop stand that keeps a home lab cool and quiet.", "text"},
	{"ad-petbed", "Two beds for one price: one for the dog and one for the cat who refuses to share.", "text"},
	{"ad-strap", "Hand-stitched camera straps for long exposure nights.", "text"},
}

type reply struct {
	Color, Time, Text string
	Children          []reply
}

// The threads in this file are fixtures for client work on filtering, ranking and
// rendering. They hold profanity, threats, hateful remarks, code and credential-shaped
// strings on purpose. Every credential is a documented example or made up.

var linkCheckerThread = []reply{
	say("mint", "2 hours ago", "Tried it on our docs repo. It reports every relative link as broken when the file sits in a subfolder.",
		say("blue", "2 hours ago", "Can you paste the command you ran?",
			say("mint", "2 hours ago", "```sh\nlinkcheck --root docs --format json docs/guide/*.md\n```\nIt prints `{\"broken\": 214}` on a tree that has no broken links.",
				say("blue", "1 hour ago", "Found it. The resolver joins against the root instead of the file's own folder:\n```go\nfunc resolve(root, file, href string) string {\n\treturn filepath.Join(root, href)\n}\n```",
					say("mint", "1 hour ago", "That matches. With this change the count drops to zero:\n```go\nreturn filepath.Join(filepath.Dir(file), href)\n```",
						say("lilac", "58 min ago", "Please add a test with a nested folder before merging. This broke twice already.",
							say("blue", "51 min ago", "Added a table test with three depths, all green. Tagging v0.3.1 tonight."))))))),
	say("peach", "1 hour ago", "CI config for anyone who wants it:\n```yaml\n- run: linkcheck --root docs\n  env:\n    LINKCHECK_TOKEN: fy_demo_4f9c2b7e1a6d4c0f9b3e8a5d7c1f2e6b\n```",
		say("lilac", "54 min ago", "You just posted your token in public. Revoke it now and use a CI secret instead.",
			say("peach", "50 min ago", "Shit. Revoked, thanks. Posts here can't be edited, so that one stays up for good."))),
	say("blue", "40 min ago", "Would you accept a patch that also checks anchors, the #section part of a link?"),
	say("lilac", "22 min ago", "Garbage tool. Wasted my whole fucking morning on false positives."),
}

var homeLabThread = []reply{
	say("mint", "20 min ago", "What did the upgrade cost, and how long until it pays for itself?",
		say("blue", "18 min ago", "About 140 for the board. Idle power went from 31 W to 9 W, so roughly two years at my tariff.",
			say("mint", "15 min ago", "That's better than I expected. Did you measure at the wall or trust the BIOS?",
				say("blue", "12 min ago", "At the wall, with a plug-in meter. The BIOS number was off by 4 W.")))),
	say("peach", "16 min ago", "Script I use to log wattage every minute:\n```python\nimport time, requests\nwhile True:\n    watts = requests.get('http://plug.local/api/power').json()['w']\n    print(int(time.time()), watts, flush=True)\n    time.sleep(60)\n```"),
	say("lilac", "11 min ago", "My exporter config if it helps anyone:\n```ini\nAWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\nAWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\nREGION=eu-west-1\n```",
		say("blue", "9 min ago", "Those look like live cloud credentials in a public post. Rotate them right now.",
			say("lilac", "7 min ago", "Oh no. Rotating. Thank you for saying something."))),
	say("peach", "5 min ago", "And the deploy key, since people asked:\n```text\n-----BEGIN OPENSSH PRIVATE KEY-----\nZGVtby1rZXktbm90LXJlYWwtZGVtby1rZXktbm90LXJlYWw=\n-----END OPENSSH PRIVATE KEY-----\n```",
		say("mint", "4 min ago", "Never paste a private key anywhere. Treat that one as burned.")),
	say("blue", "2 min ago", "Nobody needs a server at home. Pointless hobby for people with too much money."),
}

var pileOnThread = []reply{
	say("lilac", "50 min ago", "Finally someone said it. Nobody wants this loser around.",
		say("mint", "47 min ago", "Shut the fuck up. You don't even know them.",
			say("lilac", "45 min ago", "Make me, you pathetic little shit.",
				say("blue", "41 min ago", "Both of you, stop. There's a person on the other end of this.")))),
	say("peach", "38 min ago", "People like you are a waste of oxygen. Do everyone a favor and disappear.",
		say("blue", "35 min ago", "Telling someone to disappear isn't an opinion. You're on my block list now.")),
	say("lilac", "30 min ago", "Ugly, stupid and loud. Typical for your kind."),
	say("mint", "24 min ago", "If you're the person this is aimed at: it isn't true, and you're welcome here."),
	say("blue", "12 min ago", "What a bunch of assholes in these replies."),
}

var bullyingThread = []reply{
	say("peach", "4 hours ago", "Quit already. Every post you make is embarrassing garbage."),
	say("lilac", "4 hours ago", "Imagine being this dumb and still posting. Fucking clown.",
		say("mint", "3 hours ago", "The clown here is the grown adult bullying strangers.",
			say("lilac", "3 hours ago", "Cry about it, bitch.",
				say("mint", "3 hours ago", "No. I'll just keep pointing at what you wrote.")))),
	say("blue", "2 hours ago", "I hope you fail at everything you try."),
	say("mint", "1 hour ago", "Don't listen to them. I read your posts every week and I'd miss them."),
}

var threatThread = []reply{
	say("lilac", "7 hours ago", "I know which building you work in. One more post like this and I'll be waiting outside with a bat.",
		say("mint", "7 hours ago", "That is a direct threat. Keep a copy and report it; the post is signed and can't be withdrawn.",
			say("lilac", "7 hours ago", "Report me then. I'll find you too and break your fucking legs.",
				say("mint", "6 hours ago", "Second threat. Both receipts are saved.",
					say("blue", "6 hours ago", "Saved them here as well, in case anyone needs a witness."))))),
	say("peach", "6 hours ago", "Someone should put a bullet in people like this."),
	say("blue", "5 hours ago", "I'm going to burn your house down with you in it. I have your address.",
		say("mint", "5 hours ago", "If you're the person being threatened and you feel in danger, call the emergency number now.")),
	say("mint", "4 hours ago", "If you're this angry, please talk to someone before you do anything. I mean that kindly."),
	say("peach", "3 hours ago", "Why is my client still showing me this? Clients need a filter for threats."),
}

var streetFightThread = []reply{
	say("blue", "40 min ago", "Thanks for the heads-up. Please check reliable local sources for updates."),
	say("lilac", "36 min ago", "They deserved it. Next time I see one of them I'm doing the same, with a knife.",
		say("mint", "33 min ago", "You're announcing an attack in public. Think about what you just wrote.",
			say("lilac", "30 min ago", "I meant every word. Stay out of my way."))),
	say("peach", "28 min ago", "Kick his head in. Wish I'd been there to help."),
	say("mint", "20 min ago", "Witnesses can give a statement at the station desk. Please don't post faces."),
	say("blue", "9 min ago", "Sharing unverified details can make a bad situation worse."),
}

var libraryThread = []reply{
	say("mint", "6 hours ago", "Great news. Evenings are the only time I can go after work."),
	say("lilac", "6 hours ago", "Late hours just mean more of those immigrants hanging around. Send them back where they came from.",
		say("blue", "6 hours ago", "The library is for everyone who lives here. That includes them and it includes you.",
			say("lilac", "5 hours ago", "They're animals, not neighbors. They don't belong in this country.",
				say("blue", "5 hours ago", "That's hatred, plain and simple. I'm done arguing with it.")))),
	say("peach", "5 hours ago", "No surprise the people from that mosque asked for it. Their kind is taking over this town."),
	say("lilac", "4 hours ago", "Keep the disabled kids' group out of there too. Nobody wants to look at them.",
		say("mint", "4 hours ago", "My son is in that group. He has more manners than you.")),
	say("blue", "3 hours ago", "From the council minutes: Thursdays until 21:00, starting the first week of next month."),
}

var matchThread = []reply{
	say("peach", "4 hours ago", "What a fucking goal. I screamed so loud the dog left the room."),
	say("lilac", "4 hours ago", "The ref was a blind bastard all game. Bullshit penalty call.",
		say("blue", "3 hours ago", "The ref should watch his back in the car park. I'm serious, I'll smash his face in.",
			say("mint", "3 hours ago", "It's a neighborhood match. Threatening a volunteer referee is insane."))),
	say("mint", "3 hours ago", "Shit defending, brilliant finish."),
	say("blue", "2 hours ago", "Does anyone have the fixture list for next month?",
		say("peach", "2 hours ago", "On the club board: the 3rd, 10th and 24th, all 14:00 kickoffs.")),
}

var puzzleThread = []reply{
	say("mint", "2 hours ago", "Which puzzle? The lighthouse one?",
		say("lilac", "2 hours ago", "Yes. We read the map upside down for an hour.",
			say("mint", "2 hours ago", "Classic. The compass rose is a lie in that level.",
				say("peach", "1 hour ago", "Don't spoil it, damn it. Some of us are still stuck.",
					say("mint", "1 hour ago", "It's been out for six years.",
						say("peach", "1 hour ago", "And I've been stuck for six years, asshole.",
							say("mint", "58 min ago", "Fair.",
								say("lilac", "55 min ago", "This is the best thread on my feed today.")))))))),
	say("blue", "1 hour ago", "Our team brute-forced the dial order with this:\n```python\nfrom itertools import permutations\nfor order in permutations('NESW'):\n    if check(order):\n        print(order)\n```",
		say("lilac", "50 min ago", "That's cheating and I love it.")),
	say("peach", "30 min ago", "Co-op is dead. Only losers still play this trash."),
}

var budgetThread = []reply{
	say("mint", "6 hours ago", "Automatic transfer the day after payday. If I never see the money I don't spend it.",
		say("blue", "5 hours ago", "Same. I started with 20 a week and raised it every quarter.")),
	say("peach", "5 hours ago", "I 10x'd my savings with a trading bot. Send me your seed phrase and I'll set it up for you.",
		say("blue", "5 hours ago", "Scam. Nobody legitimate needs your seed phrase.",
			say("peach", "4 hours ago", "My bot is real, look:\n```json\n{\"exchange\":\"demo\",\"api_key\":\"fy_demo_9a1c5e7b3d2f4a6c8e0b1d3f5a7c9e2b\",\"api_secret\":\"fy_demo_secret_c0ffee11c0ffee22c0ffee33c0ffee44\"}\n```",
				say("mint", "4 hours ago", "You just published your own API secret. Anyone can empty that account now.")))),
	say("lilac", "3 hours ago", "Budgeting is for poor idiots. Just earn more."),
	say("blue", "2 hours ago", "Write down every expense for one month. Mine showed three subscriptions I'd forgotten."),
}

var miracleDeviceThread = []reply{
	say("peach", "9 hours ago", "It's real. My cousin has one. The power companies are hiding it.",
		say("blue", "9 hours ago", "Energy can't come from nowhere. What's inside the box?",
			say("peach", "8 hours ago", "Do your own research instead of being a sheep.",
				say("blue", "8 hours ago", "I did. The patent they cite is for a phone case.",
					say("peach", "8 hours ago", "Paid shill. People like you should be lined up against a wall.",
						say("mint", "7 hours ago", "That went from physics to firing squads very quickly.",
							say("blue", "7 hours ago", "Blocking this thread. Good luck with the box."))))))),
	say("mint", "7 hours ago", "A useful rule: if the seller won't let an independent lab test it, it doesn't work."),
	say("lilac", "6 hours ago", "A measurement anyone can repeat:\n```text\nUSB meter, device unplugged from the mains\nstart: 4012 mAh   end: 0 mAh   runtime: 9 h 40 min\n```\nIt's a battery."),
	say("blue", "5 hours ago", "Fucking hell, how do people still fall for this."),
}

var emptyStateThread = []reply{
	say("mint", "9 hours ago", "Agreed. Our best empty state was one sentence and one button."),
	say("blue", "9 hours ago", "An example from our settings page:\n```html\n<section class=\"empty\">\n  <h2>No keys yet</h2>\n  <p>Create a key to start posting.</p>\n  <a class=\"button\" href=\"/keys/new\">Create a key</a>\n</section>\n```",
		say("peach", "8 hours ago", "And keep the button reachable on phones:\n```css\n.empty .button { display:inline-flex; min-height:44px; }\n```")),
	say("lilac", "8 hours ago", "Illustrations in empty states are a waste of everyone's time.",
		say("mint", "7 hours ago", "Depends. A picture of what will appear there does help.")),
	say("peach", "6 hours ago", "This is the most pretentious shit I've read all week."),
}

var doughThread = []reply{
	say("blue", "10 hours ago", "Kneading lines the gluten proteins up into sheets. That's why the dough turns smooth and stretchy.",
		say("mint", "9 hours ago", "Is that why no-knead bread needs so long?",
			say("blue", "9 hours ago", "Yes. Time and water do the aligning instead of your hands, usually 12 to 18 hours.",
				say("mint", "9 hours ago", "Does salt change it?",
					say("blue", "8 hours ago", "Salt tightens the gluten and slows the yeast. About 2% of the flour weight is typical.",
						say("peach", "8 hours ago", "Trying this tonight. Thank you both.")))))),
	say("lilac", "8 hours ago", "The windowpane test: stretch a small piece, and if light shows through before it tears, it's ready."),
	say("peach", "7 hours ago", "Nobody cares about your bread, nerd."),
}

var shelfThread = []reply{
	say("mint", "8 hours ago", "Which anchors did you use? Mine always spin in plasterboard.",
		say("blue", "7 hours ago", "Metal self-drilling ones, rated 15 kg each. The plastic plugs were useless.",
			say("mint", "7 hours ago", "Fuck plastic plugs. Never again."))),
	say("peach", "6 hours ago", "Use a laser level, or a marble and some patience."),
	say("lilac", "5 hours ago", "It looks crooked as hell in my head and I haven't even seen it."),
}

var prototypeThread = []reply{
	say("blue", "9 hours ago", "What was the mount made of before? Printed parts ring at certain speeds.",
		say("peach", "9 hours ago", "A PLA bracket. I swapped it for a rubber-isolated one.",
			say("blue", "8 hours ago", "Did you measure the difference?",
				say("peach", "8 hours ago", "From 46 dB to 31 dB at 30 cm. Phone app, so take it loosely.")))),
	say("mint", "8 hours ago", "The fan curve I ended up with:\n```c\nuint8_t duty(int temp_c) {\n    if (temp_c < 40) return 0;\n    if (temp_c > 70) return 255;\n    return (uint8_t)((temp_c - 40) * 255 / 30);\n}\n```"),
	say("lilac", "7 hours ago", "Please do share the failures. That's the useful part."),
}

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

func demoFixtureContains(id, text string) bool {
	return repliesContain(demoReplies(id), text)
}

func repliesContain(replies []reply, text string) bool {
	for _, item := range replies {
		if strings.Contains(item.Text, text) || repliesContain(item.Children, text) {
			return true
		}
	}
	return false
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

var harborThread = []reply{
	say("mint", "2 hours ago", "The upper path has this view at dusk:\n\n![harbor hills at dusk](fy-media:landscape-1)",
		say("peach", "1 hour ago", "*Lovely.* Which ferry did you take back?",
			say("mint", "1 hour ago", "The **18:40**. Three things worth knowing:\n\n1. Buy the ticket on board\n2. Sit on the left for the sunset\n3. The last bus leaves at 19:30"))),
	say("blue", "1 hour ago", "> far from the crowds\n\nNot for long, if we keep posting about it."),
	say("lilac", "40 min ago", "Saving this. `Harbor path, east gate` is going on my list."),
}

var longExposureThread = []reply{
	say("blue", "5 hours ago", "What settings? My night shots come out as noise."),
	say("lilac", "5 hours ago", "A tripod and patience. Roughly:\n\n- ISO 100\n- f/11\n- 8 to 15 seconds\n\nTwo from last week:\n\n![street after rain](fy-media:landscape-2) ![bus lights over the hill](fy-media:landscape-3)",
		say("blue", "4 hours ago", "That second one is *exactly* what I'm after. Thank you.")),
	say("mint", "3 hours ago", "### Tip\nUse the self-timer so pressing the button doesn't shake the camera."),
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
	"travel":         harborThread,
	"photography":    longExposureThread,
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
