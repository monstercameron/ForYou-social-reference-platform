package server

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
