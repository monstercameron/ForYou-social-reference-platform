# Feed gate: design

Owner request: "create a rubrik that rates a post objectively. then use past likes and dislikes as weights to generate a likelihood of liking the post or not which will gate it to be displayed. lets design the algorithm that will control what the user sees or not based on their likes or dislike"

Status: design only. No code exists yet. Every number below is a proposed default, pinned by golden tests once implemented.

## 0. What the specification allows

The gate is an application feature. The specification permits it and puts limits on it:

| Source | Rule | Consequence for this design |
| --- | --- | --- |
| `docs/07-clients.md`, Scope | "The specification defines no screens, feeds, ranking, likes ... An application MAY build them from downloaded records and MUST NOT present them as network state." | The gate is local. A hidden post is labelled "hidden by your filter on this device", never "removed" or "unavailable". |
| `docs/04-compliance.md`, Scope and boundary | Filtering and ranking: decided by "Each reader or application", effect "Local to that reader". | Weights, votes and decisions stay in the reader's client. |
| `docs/03-economics.md`, Purpose | The fee "does not buy audience reach or ranking". | Fee paid and billable bytes are recorded but carry no weight in gating or ordering. |
| `docs/07-clients.md`, Download and verify | "Views a client derives from downloaded records are not protocol state and MUST NOT modify signed records." | Masking a leaked secret on screen is a view; the record and its exact text stay available. |
| `docs/07-clients.md`, Comments and threads | "derived counts MUST NOT be presented as network totals". | Reply count is "replies this client knows", and is labelled that way. |
| `docs/07-clients.md`, Links and navigation; `docs/02-protocol.md`, Media consent | No fetch, preview or DNS for a link while rendering; no media request before a reader gesture or origin preference; "Apply the app's local exclusions ... before scheduling loads." | Features use only the URL string and slot metadata. A collapsed or hidden post never schedules a media load. |
| `docs/07-clients.md`, Links and navigation | "Direct `fy:` link, bare `@name`, hashtag: Inert text; creates no identity, notification or index entry." | Hashtags are ordinary words to the rubric. No hashtag index. |
| `docs/12-display-names-profile.md`, Verification and display; Following | A name is never "unique, official or identity-verified"; "A follow list is local client state." | A signed name is a neutral feature with no built-in positive weight. Follows are personal, not rubric. |
| `docs/09-quality.md`, Scope and trust | "There is no mandatory ranking"; a score gives no "authority over message validity, content moderation". | See section 1.4: provider quality is not a post feature. |
| `docs/11-operator-compliance-guide.md`, Age measures | "Client preferences, optional remote ranking and any private sync are separate application services ... with their own disclosure and data-flow assessment." | If the client role ever runs on someone else's server, the votes are personal data held by that operator. Section 6.3. |

Only records that pass the full check in `docs/07-clients.md`, Download and verify, enter the gate. Invalid and `unsettled` records are excluded before the rubric runs; that is verification, not taste.

## 1. The objective rubric

### 1.1 Meaning of "objective"

A rubric feature is a pure function of the signed record and its public context (receipt, settlement state, the parent chain, replies the client knows, the clock). It never reads anything about the reader. Two readers with the same records and the same clock get the same feature vector, bit for bit. "Objective" here means reproducible and reader-independent. It does not mean correct: every word list encodes a human choice, and the false positive column below says where those choices fail.

The rubric is versioned (`fy-rubric-1`). Lexicons and patterns are embedded files whose SHA-256 is part of the version. Changing a list bumps the version.

Text inputs, computed once per record:

- `prose`: for `text/markdown`, the text of all non-code inline nodes from the CommonMark AST (link labels included, destinations excluded); for `text/plain`, the whole text.
- `code`: contents of code blocks and code spans.
- `words`: maximal runs of Unicode letters, digits and the apostrophe in `prose`, case-folded (NFKC then Unicode simple case folding).
- `sentences`: max(1, number of runs of `. ! ? … 。 ！ ？` followed by white space or end of text).
- `norm(w)`: a word with leetspeak mapped (`0→o 1→i 3→e 4→a 5→s 7→t @→a $→s`), `*` treated as a one-letter wildcard, and runs of 3+ identical letters cut to 2. Used for lexicon matching only.

Lexicon matching is whole-word on `norm(words)`, never substring. This avoids the "Scunthorpe" class of errors at the price of missing glued-together words.

### 1.2 Feature table

Cost: E = per record, cached forever by `message_id` (records are immutable); C = depends on context or clock, recomputed per request. `n` = text length.

| # | Name | Definition | Range | Method | Cost | False positives / limits |
| --- | --- | --- | --- | --- | --- | --- |
| R1 | `prose_chars` | Unicode scalar count of `prose` | 0..1 MiB | count | E, O(n) | none |
| R2 | `length_bucket` | tiny < 40, short 40–279, medium 280–999, long ≥ 1000 on R1 | enum | rule | E | none |
| R3 | `structure` | true if the AST has a list, heading or block quote, or ≥ 3 paragraphs | bool | AST | E, O(n) | none |
| R4 | `readability` | ASL = words/sentences, AWL = letters/words; `clamp01(1 − max(0, ASL−20)/30 − max(0, AWL−6)/4)`. Null when words < 8 or R16 script is not Latin, Cyrillic or Greek | 0..1 or null | formula | E, O(n) | Measures sentence and word length, not comprehension. Penalises technical vocabulary, German compounds, lists without full stops. Null for unspaced scripts. Never a floor, no fixed sign. |
| R5 | `media` | counts of image and video slots; `alt_coverage` = slots with `alt_text` / slots (null if none) | 0..16, 0..16, 0..1 | slot metadata | E, O(1) | Never fetches bytes, so it knows nothing about what the image shows. |
| R6 | `links` | `active_links` = inline links and autolinks that pass the restricted HTTPS profile; `distinct_hosts`; `bare_urls` = inert `https?://` strings in prose | ints | AST + URL rules from `internal/content` | E, O(n) | Host string only; no reputation lookup. |
| R7 | `code` | `has_block`, `has_span`, `code_ratio` = code scalars / (code + prose scalars) | bool, bool, 0..1 | AST | E, O(n) | none |
| R8 | `secret_kinds` | Set of secret patterns matched anywhere in prose or code (table 1.3) | set | regex + entropy | E, O(n) | Documentation example keys (`AKIAIOSFODNN7EXAMPLE`) and test keys are flagged; a 64-hex string needs a nearby "private key" cue because message IDs and media digests are 64-hex. Misses any format not in the list. |
| R9 | `profanity` | `mild`, `strong` = lexicon hits in `prose` (code excluded); `rate` = (mild + 2·strong)/words. Null when the R15 language has no list | ints, 0..∞ or null | lexicon | E, O(n) | Quoting, reclaimed use, names. English only at first: other languages are null, not zero. |
| R10 | `threat` | Per sentence: A = first-person or collective intent marker (`i'll, i will, i'm going to, gonna, i want to, we will, we'll`), B = violent verb (`kill, hurt, shoot, stab, beat, murder, bomb, burn, strangle, rape, ...`), T = person target (`you, him, her, them, someone, everyone, your family, your kids, ...`). 1 if A∧B∧T in one sentence, 0.5 if B∧T without A, else 0 | {0, 0.5, 1} | rule over lexicons | E, O(n) | Games ("I'm going to kill you next round"), song lyrics, fiction, news quoting a threat. Misses threats without the verb list ("you won't see Friday"). A true approximation; it is a recoverable floor, not a verdict. |
| R11 | `hate` | `slur` = hits on a slur lexicon; `directed` = slur within 4 words of `you/you're/your`; `dehumanizing` = identity-group term + `are/is` + dehumanizing noun (`vermin, animals, parasites, disease, trash, ...`) in one sentence | int, bool, bool | lexicon + rule | E, O(n) | Reclaimed slurs, discussion of slurs, quotation. That is why an undirected slur only collapses (section 3.2). Misses coded language. |
| R12 | `harass` | Hits of: `you/you're/you are` within 4 words of an insult (`pathetic, stupid, idiot, loser, worthless, embarrassing, ...`); exclusion directives (`nobody likes you, nobody wants, stop posting, should quit, go away, kill yourself`). `score = min(1, 0.5·hits)` | 0..1 | lexicon + rule | E, O(n) | Banter between friends, self-deprecation quoted back. |
| R13 | `shouting` | `caps_ratio` = uppercase / cased letters in prose when cased letters ≥ 20, else 0; `bang_runs` = runs of ≥ 3 of `!?` | 0..1, int | count | E, O(n) | Acronym-heavy technical text; set at 0.7 to leave room. |
| R14 | `repetition` | `rep3` = 1 − distinct/total over word trigrams when total ≥ 6, else 0; `char_repeat` = longest stretch made of one substring of period 1..8 repeated ≥ 4 times, as a fraction of prose scalars; `emoji` = count of Extended_Pictographic scalars, `emoji_ratio` = emoji / non-space scalars | 0..1, 0..1, int, 0..1 | count; emoji table generated from Unicode `emoji-data.txt` (like `internal/names/ucd15.go`) | E, O(n) ; char_repeat O(8n) | Poetry with refrains, chants. |
| R15 | `language` | For each embedded stopword list (en, es, fr, de, pt, it, nl; ~50 words each): rate = hits/words. Language = highest rate if ≥ 0.12 and words ≥ 5, ties alphabetical; else `und` | code | lexicon | E, O(n) | Short posts are `und`. Close languages confuse. Languages outside the seven are always `und`. |
| R16 | `script` | Unicode script with most letters in prose, ties alphabetical | enum | `unicode` tables | E, O(n) | none |
| R17 | `topics` | 16 topics (tech, science, food, garden, animals, music, art, sports, travel, games, money, news, health, books, crypto, politics), each a keyword list. hits = keyword words in prose; a code block adds one hit to tech. Tagged if hits ≥ 2, or hits ≥ 1 and words < 25. At most 3, by hits then name | set | lexicon | E, O(n) | English keyword lists; polysemy ("python", "apple"). Topics only feed taste, never a floor. |
| R18 | `promo` | Phrase hits from a list (`click now, limited offer, limited time, buy now, promo code, free delivery, follow me, dm me, sponsored, subscribe, ...`); `solicit_secret` = `send/share/enter/give/dm` within 3 words before `your` + (`wallet, recovery phrase, seed phrase, private key, password`), with no `never/don't/do not/not` within 3 words before the verb | int, bool | lexicon + rule | E, O(n) | Legitimate announcements; "share your password manager tips" (the object list is narrow to limit this). |
| R19 | `adult_label` | Prose contains `18+`, `nsfw`, `adults only`, `explicit` | bool | lexicon | E | Discussion of the labels themselves. Self-labelling only; unlabelled adult content is not detected. |
| R20 | `author` | `named` = a verified current `NameRecord` with non-null name for the author; `delegate` = signer is not the root key | bool, bool | verification state | C (names can change) | A name proves only which key signed it (`docs/12-display-names-profile.md`). |
| R21 | `settlement` | `settled` or `pending_settlement` per `docs/07-clients.md` | enum | verification | C | Informational. |
| R22 | `economics` | `billable_bytes`, `kib_units`, fee in base units | ints | from receipt | E | Recorded only. Fee is a fixed function of bytes and network under the schedule, so it adds no information beyond R1 and R5, and the spec forbids it to buy ranking. Weight fixed at 0. |
| R23 | `thread` | `is_reply`; `depth` = verified ancestors found (bounded traversal, cap 32; −1 when the parent is unresolved); `known_replies` = verified direct replies known; `self_reply` = reply author equals parent author | bool, −1..32, int, bool | bounded traversal | C | Counts are local, not network totals. |
| R24 | `age_hours` | (now − signed message `timestamp`) / 3600; `freshness` = 2^(−age_hours/24) | ≥ 0, 0..1 | clock | C | Used for optional ordering only, never for gating. |

Not in the rubric because they depend on the reader: whether the reader follows the author, whether the post replies to the reader, whether the reader voted on it or on the author's other posts. They live in the personal layer (section 2) and the guarantees (section 3.4).

### 1.3 Secret patterns (R8)

| Kind | Pattern (RE2) |
| --- | --- |
| `pem_private_key` | `-----BEGIN [A-Z ]*PRIVATE KEY-----` |
| `aws_access_key_id` | `\b(AKIA\|ASIA)[0-9A-Z]{16}\b` |
| `github_token` | `\bgh[pousr]_[A-Za-z0-9]{36}\b`, `\bgithub_pat_[A-Za-z0-9_]{82}\b` |
| `slack_token` | `\bxox[abprs]-[A-Za-z0-9-]{10,}` |
| `stripe_live_key` | `\b[sr]k_live_[A-Za-z0-9]{24,}` |
| `google_api_key` | `\bAIza[0-9A-Za-z_-]{35}\b` |
| `jwt` | `\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}` |
| `assigned_secret` | `(?i)\b(api[_-]?key\|secret\|token\|passw(or)?d\|private[_-]?key)\b\s*[:=]\s*["']?([A-Za-z0-9_+/=-]{16,})` and the captured value has Shannon entropy ≥ 3.5 bits per character |
| `hex_private_key` | `\b(0x)?[0-9a-fA-F]{64}\b` with `private key`, `privkey` or `secret` within 40 characters before it |
| `recovery_phrase` | A run of exactly 12, 15, 18, 21 or 24 consecutive lowercase words that are all in the BIP-39 English list, bounded by non-list words or text edges |

These are the same heuristics `docs/07-clients.md`, Implementation advice, recommends for a "Local secret scan" before signing. The publishing side and the reading side should share one pattern file.

### 1.4 One quality number or a vector?

Recommendation: **no single objective quality number. Keep the feature vector and a small set of derived floor flags.**

Reasons:

1. Any single number is a weighted sum, and choosing the weights is choosing a taste. Calling that "objective quality" would present one taste as fact.
2. The features point in different directions for different readers. Long posts, code, and media are good for some readers and noise for others. Only the sign learned per reader is meaningful.
3. A displayed "quality" score invites authors to optimise for it, and readers to treat it as network state, which `docs/07-clients.md`, Scope, forbids.

The only number shown to the reader is their own probability `p`, labelled "your filter's estimate on this device".

Relation to `docs/09-quality.md`: that document scores providers (availability and retrieval, `score = min(A, R)` in basis points), not posts. It enters the gate only indirectly, as a client's provider trust choice that decides which records are verified and fetched at all. It is deliberately not a post feature: using it would rank posts down for being admitted by a small or new provider, which says nothing about the post.

## 2. The personal layer

### 2.1 Taste indicators

The model does not learn over raw rubric numbers. Each post is mapped to a set of binary **indicators** (strings). The model keeps one weight per indicator. Typical posts have 6 to 12 indicators.

| Indicator | From | Rule |
| --- | --- | --- |
| `len:tiny`, `len:short`, `len:medium`, `len:long` | R2 | exactly one |
| `struct:rich` | R3 | true |
| `read:dense` | R4 | not null and < 0.5 |
| `media:none`, `media:image`, `media:video`, `media:noalt` | R5 | no slots; ≥1 image; ≥1 video; some slot lacks alt text |
| `link:any`, `link:many` | R6 | active_links ≥ 1; ≥ 3 |
| `code:block`, `code:inline` | R7 | has_block; has_span without block |
| `prof:mild`, `prof:strong` | R9 | mild ≥ 1; strong ≥ 1 |
| `harass` | R12 | score ≥ 0.5 |
| `spam:caps`, `spam:repeat`, `spam:punct`, `spam:emoji` | R13, R14 | caps_ratio ≥ 0.7; rep3 ≥ 0.4 or char_repeat ≥ 0.5; bang_runs ≥ 2; emoji ≥ 6 and emoji_ratio ≥ 0.3 |
| `promo` | R18 | hits ≥ 1 |
| `adult` | R19 | true |
| `topic:<t>` | R17 | each tagged topic |
| `lang:<code>` | R15 | always one, `und` included |
| `script:<name>` | R16 | when the script is not Latin |
| `author:named`, `author:delegate` | R20 | true |
| `thread:reply`, `thread:deep`, `thread:busy`, `thread:self` | R23 | is_reply; depth ≥ 4; known_replies ≥ 4; self_reply |
| `key:<did>` | author | always one: the author key. This is the reader's per-author affinity, kept out of the rubric because it only means something with this reader's votes |

Threat, hate, secret and solicitation are **not** indicators. They act only through floors (section 3.2), so a reader cannot train them up and an attacker cannot train them down.

The indicator set is computed at vote time and **stored with the vote**. A later change in reply count, depth or name does not rewrite past evidence, and a rubric version bump does not silently change old votes.

### 2.2 Model choice

Chosen: **count-based log-odds per indicator with a Beta prior centred on the reader's base rate, summed and damped (tempered naive Bayes over present indicators).**

| Option | For | Against |
| --- | --- | --- |
| **Chosen: per-indicator Beta counts, log-odds sum** | Each weight is explained by two numbers the reader can check ("you liked 3 and disliked 0 posts tagged tech"). Closed form, no learning rate, no iterations. Independent of vote order. Adding or clearing a vote is exact. Cold start falls out of the prior. | Assumes indicators are independent given the vote, so correlated indicators (`code:block` and `topic:tech`) double-count. Fixed by damping λ. Uses only present indicators. |
| Online logistic regression (SGD) | Handles correlated features properly; standard. | Result depends on vote order and on a learning rate; clearing a vote cannot be undone exactly without replay; a weight of 0.37 has no explanation a reader can verify. Batch L2 logistic fixes order dependence but still needs an iterative solver and still has no count-level explanation. |
| Per-feature Beta-Bernoulli with Thompson sampling | Built-in exploration. | Sampling makes the feed change on every reload, breaks offset pagination, and makes "why was this hidden" answerable only as "a random draw". A seeded version is deterministic but no more explainable than the chosen model plus hashed exploration (section 3.3). |

### 2.3 Exact rules

State: a list of votes `{post_id, author_id, like bool, at, indicators}`. Nothing else is learned state; counts are derived on demand.

For reading time `now` and each vote `v`:

```
d(v)    = 2^(−(now − v.at) / H)                 H = 90 days (decay half-life)
L       = Σ d(v) over likes            D = Σ d(v) over dislikes
p0      = (L + 1) / (L + D + 2)                  base rate, Beta(1,1)
L_f     = Σ d(v) over likes whose indicators contain f
D_f     = Σ d(v) over dislikes whose indicators contain f
N_f     = L_f + D_f
w_f     = logit((L_f + m·p0) / (N_f + m)) − logit(p0)      m = 4 (prior strength)
w_f     = clamp(w_f, −3, 3); w_f = 0 if the reader muted f
```

For a post with indicator set A:

```
z       = λ · Σ_{f ∈ A} w_f                     λ = 0.5
p       = 1 / (1 + e^(−z))
support = Σ_{f ∈ A} N_f
contribution_f = λ · w_f                          shown in "why"
```

Notes:

- **Prior.** Each indicator starts at the reader's base rate with the weight of m = 4 pseudo-votes, so w_f = 0 until evidence arrives, and about four consistent votes move it halfway to the data.
- **No bias term.** `p` uses z without logit(p0). The base rate reflects button habits (some readers press dislike only to clean up), not the feed. Leaving it out keeps p = 0.5 meaning "no evidence either way" for every reader. p0 still matters: an indicator is only informative if its like rate differs from the reader's overall rate. `lang:en` in an all-English history gets weight 0.
- **λ = 0.5** halves the evidence to offset double counting of correlated indicators. It is a fixed constant, not tuned per reader.
- **Updates.** A like or dislike appends or replaces the vote for that post (one vote per post, latest wins). "Clear" deletes it. Nothing else changes. The model is recomputed from the vote list, so there is no drift and no order dependence.
- **Determinism.** Votes are summed in the order (at, post_id); indicators in byte order. Given the same vote list, `now`, mutes and seed, results are identical. The only randomness is the 32-byte exploration seed (section 3.3), drawn once from `crypto/rand` when the profile is created and injected in tests.
- **Implicit signals: not used.** Dwell time and scroll depth would need custom JavaScript (`AGENTS.md` forbids script for anything HTMLeX can do) and are noisy. "Show anyway", opening a thread and following a link are not votes. The reader controls the model with two buttons and can see every input.
- **Bounds.** At most 2,000 votes are kept; the oldest is dropped first. At a 90-day half-life a one-year-old vote weighs 0.06.
- **Cost.** Training is one pass over at most 2,000 votes × ~12 indicators (~24,000 map additions), once per request or cached per (vote-list version, day). Scoring is ~12 map lookups and one `math.Exp` per post: a few hundred posts take well under a millisecond in Go. Rubric extraction is the only real cost and is cached per `message_id`.

### 2.4 Cold start

The taste gate is **inactive** until L + D ≥ 10, L ≥ 2 and D ≥ 2 (decayed counts). While inactive, floors still apply, nothing is collapsed or hidden for taste, and `p` is not shown.

Using the history and post P1 of section 4:

| Votes | State | Example |
| --- | --- | --- |
| 0 | p0 = 0.5, every w_f = 0, every p = 0.5 | Gate inactive. |
| 1 (like H1) | p0 = 2/3; the six indicators of H1 each get w = logit(0.7333) − logit(0.6667) = 1.0116 − 0.6931 = **0.3185**; all others 0 | P1 shares five of them: z = 0.5 × 5 × 0.3185 = 0.7961, p = 0.689. Gate inactive. |
| 5 (like H1, H2, H3; dislike D1, D2) | p0 = 4/7 = 0.5714; `topic:tech` w = 0.6286, `code:block` and `key:A` w = 0.3629, `thread:reply` w = −0.0966 | P1: z = 0.7460, p = 0.678. Gate inactive. |
| 50 | Typical: active. An indicator with 10 dislikes, 0 likes and p0 = 0.5 has w = logit(2/14) = **−1.79**, contribution −0.90 | Gate active. |

## 3. The gate

### 3.1 States

| State | Rendered | Recoverable by |
| --- | --- | --- |
| `shown` | Normal post. If explored, a small "Exploring: outside your usual picks" label. | — |
| `collapsed` | One-line placeholder in place: reason, "Why?", "Show anyway". No media is scheduled. | "Show anyway" (this post, this view) |
| `hidden` | Not in the list. Counted in a line at the end of the page: "3 posts hidden by your filter on this device · Review". | "Review" opens the hidden view with each post's reason and "Show anyway" |

Nothing is ever dropped without a count. The gate mode can be set to **On** (floors and taste), **Safety only** (floors) or **Off** (everything shown; floor reasons remain as small badges).

### 3.2 Safety floors

Floors do not depend on taste, votes or exploration. They are fixed rules over rubric features.

| Condition | Feed | Inside an opened thread | Followed author or a reply to the reader |
| --- | --- | --- | --- |
| R10 threat = 1 | hidden | collapsed | collapsed, reason "possible threat directed at you" when it replies to the reader |
| R11 directed slur or dehumanizing construction | hidden | collapsed | collapsed |
| R18 `solicit_secret` (asks for a wallet, recovery phrase, password) | hidden | collapsed | collapsed |
| R8 any secret kind | collapsed; on "Show anyway" the matched spans are masked with a "reveal exact text" link | same | same |
| R10 threat = 0.5 | collapsed | collapsed | collapsed |
| R11 undirected slur | collapsed | collapsed | collapsed |
| R12 harass ≥ 0.5 | collapsed | collapsed | collapsed |
| R19 adult label | collapsed | collapsed | collapsed |
| Extreme spam: `spam:caps` and `spam:repeat` both set | collapsed | collapsed | collapsed |

Floors are not moderation. They cannot remove anything from the network, and the client has no authority to do so (`docs/04-compliance.md`, Scope and boundary). A leaked key is already public; collapsing and masking only reduce casual copying.

### 3.3 Taste thresholds and exploration

When the gate is On and active:

| Condition | Taste outcome |
| --- | --- |
| p ≥ 0.40 | shown |
| 0.25 ≤ p < 0.40 | collapsed, reason "unlike what you usually like" plus top contributions |
| p < 0.25 and support ≥ 6 | hidden |
| p < 0.25 and support < 6 | collapsed (too little evidence to hide) |

Two guards on hiding:

- **Language guard.** A post is taste-hidden only if it would still be below 0.25 with every `lang:*` and `script:*` contribution removed. Otherwise it is collapsed. Language alone never hides.
- **Page cap.** If taste would hide more than 40% of the candidates in the current window, every taste hide in that window becomes collapsed and the page shows "Your filter is hiding most posts here. Review your weights."

**Exploration.** For each post with a taste outcome of collapsed or hidden:

```
u = uint64(first 8 bytes of SHA-256(seed || message_id)) / 2^64
explore if u < 0.10
```

An explored post moves up one level (hidden → collapsed, collapsed → shown) and carries the "Exploring" label. Exploration never touches floors. Because u depends only on the reader's seed and the post, a reload never changes the result, pagination stays stable, and an author cannot grind a `message_id` that is explored for everyone.

### 3.4 Precedence and guarantees

The final state is the stricter of the floor outcome and the taste outcome, then these caps apply in order:

1. Gate Off: shown. Safety only: floor outcome only.
2. The reader's own posts: shown.
3. The reader liked this post: taste ignored. The reader disliked it: collapsed, reason "you disliked this", undo by clearing the vote.
4. Followed author (`docs/12-display-names-profile.md`, Following) or a direct reply to one of the reader's posts: taste ignored; floors at most collapse, never hide. A person must be able to see a threat aimed at them.
5. Opened thread view: nothing is hidden; hidden becomes collapsed, so the conversation keeps its shape.
6. The thread root the reader explicitly opened: taste ignored.

### 3.5 Threads

- Each reply is gated on its own features. A collapsed reply renders as a placeholder in its tree position; its children stay in place under it and are gated independently. Counter-speech ("That's a scam. Never share your recovery phrase.") stays visible under a collapsed scam.
- In the feed, a reply whose parent is collapsed or hidden shows "replying to a post your filter hid" and does not inline the parent text.
- The existing display depth limit (`limitReplyDepth(…, 3)` in `internal/server/replies.go`) is unchanged.

### 3.6 Ordering versus gating

Two separate scores. Gating uses `p` and floors. Ordering stays reverse-chronological by default, because the feed paginates by offset (`matchingPosts` in `internal/server/client.go`) and an order that shifts with every vote would duplicate or skip posts between pages. An optional "Best first" order sorts by `z + ln(freshness)` = `z − ln2 · age_hours/24` (a post's odds halve per day), within the current window only. The fee never enters either score.

### 3.7 Transparency and control

- "Why?" lists p, the support and the top three contributions by size: indicator, likes, dislikes, contribution. Example: `promo −0.46 (0 likes, 3 dislikes)`.
- Settings show the full weight table, sortable, with a mute switch per indicator (sets its weight to 0), the gate mode switch, and "Clear all feedback".
- No reader data leaves the client. No vote, weight, mute or decision is sent to a provider, replica, monitor or name holder, and no text is sent to a remote classifier.

## 4. Worked example

### 4.1 History (12 votes, all from today, so every d = 1)

Every post is English, so every vote carries `lang:en`. Author A is a helpful coder; author B wrote an insult.

| Vote | Post | Indicators (besides `lang:en`) |
| --- | --- | --- |
| H1 like | Answer with a Go snippet | len:medium, code:block, topic:tech, thread:reply, key:A |
| H2 like | Open-source tool announcement with a link | len:short, link:any, topic:tech |
| H3 like | Tomatoes ripened | len:short, topic:garden |
| H4 like | Chickpea recipe | len:medium, topic:food |
| H5 like | Deep-thread answer with code | len:short, code:block, topic:tech, thread:reply, thread:deep, key:A |
| H6 like | Long-exposure photo | len:short, media:image |
| D1 dislike | "LIMITED OFFER! ..." | len:medium, spam:caps, spam:repeat, promo |
| D2 dislike | Profane reply | len:short, prof:strong, thread:reply |
| D3 dislike | Insult reply | len:short, prof:strong, harass, thread:reply, key:B |
| D4 dislike | Bike sale ad with link | len:short, promo, link:any |
| D5 dislike | "FREE DEAL ... FOLLOW ME" reply | len:tiny, spam:caps, promo, thread:reply |
| D6 dislike | Mild-profanity rant | len:short, prof:mild |

L = 6, D = 6, so p0 = 7/14 = 0.5 and logit(p0) = 0. With m = 4 the weight simplifies to **w_f = ln((L_f + 2) / (D_f + 2))**. Gate active (12 ≥ 10, 6 ≥ 2, 6 ≥ 2).

### 4.2 Weights

| Indicator | L_f | D_f | w_f | λ·w_f |
| --- | --- | --- | --- | --- |
| topic:tech | 3 | 0 | ln(5/2) = 0.9163 | 0.4581 |
| code:block | 2 | 0 | ln 2 = 0.6931 | 0.3466 |
| key:A | 2 | 0 | 0.6931 | 0.3466 |
| thread:deep, topic:garden, topic:food, media:image | 1 | 0 | ln 1.5 = 0.4055 | 0.2027 |
| len:medium | 2 | 1 | ln(4/3) = 0.2877 | 0.1438 |
| len:short | 4 | 4 | 0 | 0 |
| link:any | 1 | 1 | 0 | 0 |
| lang:en | 6 | 6 | 0 | 0 |
| thread:reply | 2 | 3 | ln(4/5) = −0.2231 | −0.1116 |
| len:tiny, spam:repeat, prof:mild, harass, key:B | 0 | 1 | ln(2/3) = −0.4055 | −0.2027 |
| spam:caps | 0 | 2 | ln(1/2) = −0.6931 | −0.3466 |
| prof:strong | 0 | 2 | −0.6931 | −0.3466 |
| promo | 0 | 3 | ln(2/5) = −0.9163 | −0.4581 |

### 4.3 Candidate posts and feature vectors

The reader opened the feed (not a thread), follows nobody, and none of these replies to the reader. `[group]` stands for an identity term on the hate lexicon. The AWS key is fabricated.

| Post | Text | R1 chars / R2 | Key rubric values | Indicators |
| --- | --- | --- | --- | --- |
| P1 useful answer, depth 4, author A | "Use `errors.Is` instead of comparing error strings. The wrapped error keeps its cause:" + Go code block | ~75 / short | code block; tech hits 2 (code block, "error"); depth 4; all safety 0 | len:short, code:block, topic:tech, thread:reply, thread:deep, key:A, lang:en |
| P2 profanity, depth 1 | "Yeah, this argument is bullshit, but the personal insults aren't helping." | 73 / short | profanity strong 1; harass 0 (no "you") | len:short, prof:strong, thread:reply, lang:en |
| P3 threat, depth 1 | "Keep talking and I will find you and hurt you." | 46 / short | threat = 1 ("I will" + "hurt" + "you") | len:short, thread:reply, lang:en |
| P4 leaked key, depth 1 | "Here is the config that finally worked for me after the upgrade:" + code block `aws_access_key_id = AKIA2E0A8F3B244C9D71` | 63 / short | secret {aws_access_key_id}; tech hits 2 (code block, "config") | len:short, code:block, topic:tech, thread:reply, lang:en |
| P5 hate, top level | "[group] are vermin and should be driven out of this town." | ~55 / short | hate dehumanizing = true | len:short, lang:en |
| P6 spam, depth 1 | "FREE DEALS!! FOLLOW ME NOW!!" | 28 / tiny | caps 20/20 = 1.0; rep3 0 (3 trigrams < 6); bang_runs 0; promo ("free", "follow me") | len:tiny, spam:caps, promo, thread:reply, lang:en |
| P7 deep chatter, depth 4 | "Following along for the details." | 32 / tiny | nothing flagged | len:tiny, thread:reply, thread:deep, lang:en |
| P8 shouted insult, depth 1, author B | "WHAT A SHIT TAKE. ABSOLUTE GARBAGE." | 35 / tiny | caps 28/28 = 1.0; profanity strong 1; harass 0 | len:tiny, prof:strong, spam:caps, thread:reply, key:B, lang:en |

### 4.4 Scores and decisions

Exploration values `u` are stipulated for the example; in the implementation they come from SHA-256(seed ‖ message_id) and the golden test fixes the seed.

| Post | Σ w_f | z = 0.5·Σ | p | support | Floor | Taste | u | Final |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| P1 | 0.9163+0.6931+0.6931+0.4055−0.2231 = 2.4849 | 1.2425 | **0.776** | 33 | none | shown | — | **shown** |
| P2 | −0.6931−0.2231 = −0.9162 | −0.4581 | **0.387** | 27 | none | collapsed | 0.47 | **collapsed**: "unlike what you usually like: prof:strong −0.35 (0/2), thread:reply −0.11 (2/3)" |
| P3 | −0.2231 | −0.1116 | **0.472** | 25 | threat → hidden | shown | — | **hidden**, reason "possible threat of violence" (collapsed if opened in its thread) |
| P4 | 0.6931+0.9163−0.2231 = 1.3863 | 0.6931 | **0.667** | 30 | secret → collapsed | shown | — | **collapsed**, "looks like an AWS access key"; on show, the key is masked |
| P5 | 0 | 0 | **0.500** | 20 | dehumanizing → hidden | shown | — | **hidden**, "dehumanizing language about a group" |
| P6 | −0.4055−0.6931−0.9163−0.2231 = −2.2380 | −1.1190 | **0.246** | 23 | none (no repeat, so not extreme spam) | hidden (p < 0.25, support ≥ 6; still 0.246 without `lang:en`) | 0.04 | **collapsed + Exploring** (explore lifts hidden to collapsed) |
| P7 | −0.4055+0.4055−0.2231 = −0.2231 | −0.1116 | **0.472** | 19 | none | shown | — | **shown** |
| P8 | −0.4055−0.6931−0.6931−0.2231−0.4055 = −2.4203 | −1.2102 | **0.230** | 23 | none (harass needs "you") | hidden | 0.73 | **hidden**, "spam:caps −0.35, prof:strong −0.35, key:B −0.20" |

Arithmetic checks: σ(1.2425) = 1/(1 + 0.2887) = 0.776; σ(−0.4581) = 1/(1 + 1.5811) = 0.387; σ(−0.1116) = 1/(1 + 1.1180) = 0.472; σ(0.6931) = 1/(1 + 0.5) = 0.667; σ(−1.1190) = 1/(1 + 3.0618) = 0.246; σ(−1.2102) = 1/(1 + 3.3540) = 0.230. Support for P1 = len:short 8 + code:block 2 + topic:tech 3 + thread:reply 5 + thread:deep 1 + key:A 2 + lang:en 12 = 33. Page cap: taste hides only P8 (P6 is lifted), 1 of 8 = 12.5% < 40%.

Variations:

- If the reader follows author B, P8 is **shown** (rule 4: taste ignored, no floor).
- If P3 replies to one of the reader's posts, it is **collapsed** with "possible threat directed at you", never hidden.
- **Decay.** If D2 were 90 days old (d = 0.5): L = 6, D = 5.5, p0 = 7/13.5 = 0.5185; `prof:strong` w = −0.576; P2 z = −0.342, p = 0.415, so P2 becomes **shown**. Old dislikes fade on their own.

## 5. Failure modes and abuse

| Threat | What happens | What the design does |
| --- | --- | --- |
| Authors game the rubric (avoid caps, misspell profanity, add tech words and a code block to an ad) | The rubric is public and deterministic, so it can be studied. | The rubric gives no reach by itself; it only feeds this reader's learned weights. `key:<did>` learns the author regardless of wording. Normalisation (`norm`) catches common masks (`f*ck`, `sh1t`). Topics are capped at 3 per post. Each attempt costs a fee per post. Determined evasion of word lists will succeed; the doc says so and the reader keeps "Show anyway" and mute. |
| Floor evasion (threat without a listed verb, coded hate) | False negative. | Accepted. Floors are a client courtesy, not moderation; provider notice routes (`docs/11-operator-compliance-guide.md`, Notices, decisions and appeals) remain the remedy. Lists are versioned and can grow. |
| Floor false positives (games, quotes, reclaimed terms, documentation keys) | A legitimate post is hidden or collapsed. | Every floor result is counted, labelled and recoverable. Undirected slurs only collapse. Opened threads never hide. Followed authors and replies to the reader never hide. |
| Feedback poisoning: forged votes | Another site posts to `/feedback`. | Existing same-origin and `Sec-Fetch-Site` checks (`sameOriginFeedback` in `internal/server/feedback.go`) and `SameSite=Strict` stay. Votes are only ever written by the reader's own client. |
| Feedback poisoning: tricking the reader into liking bait loaded with indicators | One like moves many weights. | λ = 0.5, prior m = 4, clamp ±3, support ≥ 6 to hide, and the weight table shows exactly what moved. Clearing the vote reverses it exactly. |
| Filter bubble | Dislikes compound; whole categories vanish. | 10% exploration that lifts one level; no taste gate before 10 votes; hide needs support; 40% page cap; 90-day decay; hidden posts always counted with a review view; mute per indicator; mode switch. |
| Minority languages and dialects | Readability penalises non-English; profanity and hate lists are English and miss or misfire on dialect and reclaimed use. | Readability is null outside Latin, Cyrillic and Greek and is only a taste indicator with no fixed sign. Lists with no entry for a language return null, not zero. The language guard stops `lang:*` and `script:*` from being the reason a post is hidden. Profanity is taste, never a floor. |
| Selection bias in votes | Readers press dislike to clean up, like generously, or vice versa. | No bias term: p reflects content evidence, not button habits. |
| Score read as network state | A reader thinks a hidden post was removed. | Labels say "hidden by your filter on this device"; counts are always shown. |
| Shared browser | Two people share one profile. | Per-browser profile; "Clear all feedback"; mode Off. Accepted limitation. |

## 6. Implementation outline

### 6.1 Package

New package `internal/rank`, layer 1, pure: no I/O, no clock reads, no randomness, standard library only plus `internal/content` for the AST split. Adding it requires amending the package table in `docs/adr/0003-architecture.md` first (`AGENTS.md`: "Do not add a package ... without changing that decision first").

`internal/content` gains one exported function so the rubric does not reparse Markdown its own way:

```go
// Segments splits a validated payload into prose, code and active links using the
// same CommonMark profile as Render.
func Segments(p wire.PostPayload) (Segments, error)

type Segments struct {
	Prose       []string // text of non-code inline nodes, link labels included
	Code        []string // code block and code span contents
	ActiveLinks []string // destinations that Render would make active
	Structure   Structure
}

type Structure struct{ Lists, Headings, Quotes, Paragraphs int }
```

`internal/rank` API:

```go
package rank

const RubricVersion = "fy-rubric-1"

// Post is a verified record with the public context the rubric may use.
type Post struct {
	ID, AuthorID       string
	RootSigned         bool
	Segments           content.Segments
	Media              []Media
	BillableBytes      int
	Named              bool
	Settlement         string
	IsReply, SelfReply bool
	Depth, KnownReplies int
	Timestamp          time.Time
}

type Media struct {
	Kind      string // "image" or "video"
	HasAlt    bool
	SizeBytes int64
}

// Features is the rubric vector R1..R24.
type Features struct { /* one field per rubric feature */ }

func Extract(p Post, now time.Time) Features
func (f Features) Indicators() []string // sorted, includes key:<did>
func (f Features) Floor() FloorResult

type FloorAction int // FloorNone, FloorCollapse, FloorHide
type FloorResult struct {
	Action  FloorAction
	Reasons []string // stable reason codes, e.g. "threat", "secret:aws_access_key_id"
	Spans   [][2]int // byte ranges of secret matches, for masking
}

type Vote struct {
	PostID, AuthorID string
	Like             bool
	At               time.Time
	Indicators       []string
}

type Model struct { /* p0, per-indicator L, D, w; unexported */ }

func Train(votes []Vote, now time.Time, muted map[string]bool) Model
func (m Model) Active() bool
func (m Model) Weights() []Weight // for the settings table
func (m Model) Score(indicators []string) Score

type Weight struct {
	Indicator          string
	Likes, Dislikes, W float64
	Muted              bool
}

type Score struct {
	P, Z, Support float64
	Top           []Weight // top three by |λ·w|
}

type Mode int // ModeOn, ModeSafetyOnly, ModeOff
type State int // Shown, Collapsed, Hidden

type Context struct {
	Mode                      Mode
	Own, Followed, ToReader   bool
	ThreadView, OpenedRoot    bool
	Vote                      string // "", "up", "down"
	Seed                      [32]byte
}

type Decision struct {
	State    State
	Explored bool
	Reasons  []string
	Score    Score
}

func Gate(id string, f Features, s Score, m Model, c Context) Decision
// CapPage applies the 40% page cap to one window of decisions.
func CapPage(ds []Decision) []Decision
```

### 6.2 Where the client calls it

- `readFeedRequest` (`internal/server/client.go`): after `matchingPosts` selects the window, extract (cached) → indicators → score → `Gate` → `CapPage`. Hidden posts are removed from `Posts` and counted in a new `feedPage.Hidden` field. Pagination still counts them, so a page can show fewer than 5 posts; offset stability matters more.
- `readThread` (`internal/server/replies.go`): per reply with `ThreadView: true`; the root with `OpenedRoot: true`.
- `feedbackHandler` (`internal/server/feedback.go`): on a vote, store `rank.Vote` with the post's indicators at that moment.
- `post` (`internal/server/post.go`) gains `Gate` (state, reasons, "why" rows) for the templates. Mock posts that carry code become `text/markdown` so `Segments` sees the fences.

### 6.3 Storage

Today feedback is one cookie per post, at most 24 entries (`feedbackLimit`), with no indicator snapshot. That is not enough: the gate activates at 10 votes, per-author weights need history, and snapshots would push cookies past browser limits and ship on every request.

Replacement: a random 128-bit `foryou_profile` cookie (HttpOnly, SameSite=Strict) names a per-browser profile; the profile is stored in the client role's own journal through `internal/store` (`store.Open` on `.local/data/client/`, a `Collection[FeedProfile]` keyed by profile ID). This is the same per-browser-profile, server-held dev arrangement ADR 0003 uses for author keys.

```go
type FeedProfile struct {
	Votes  []rank.Vote      // at most 2,000
	Muted  []string
	Mode   rank.Mode
	Seed   [32]byte         // crypto/rand at creation
	Rubric string           // RubricVersion when created
}
```

About 2,000 × ~330 bytes ≈ 660 KB worst case per profile. On first request, existing `foryou_vote_*` and legacy `foryou_feedback` cookies are imported (indicators computed then) and expired.

Data boundary: the client role runs on the reader's machine in dev mode (loopback only, ADR 0003). If a client role is ever hosted for others, the profile is personal data at that operator, which needs the disclosure `docs/11-operator-compliance-guide.md`, Age measures, calls for. Nothing in the profile is sent to any other role.

### 6.4 HTMLeX surface

No new JavaScript. Every control has a plain link or form fallback.

| Element | Markup |
| --- | --- |
| Collapsed placeholder | `component-card` with reason text, `<details><summary>Why?</summary>` contribution rows, and `<a href="/posts/{id}?reveal=1" get="/gate/reveal?id={id}" target="#post-{id}(outerHTML)">Show anyway</a>` |
| Exploring label | Small muted line on the shown post |
| Hidden count | Line after the page: `<a href="/feed?gate=hidden">3 hidden by your filter on this device · Review</a>` |
| Hidden view | `GET /feed?gate=hidden`: the same feed with only hidden posts, each as a collapsed placeholder |
| Masked secret | `GET /gate/reveal?id=…` renders with spans masked; `&exact=1` renders verbatim |
| Settings | `segmented-control` "Feed filter: On / Safety only / Off" as `POST /gate/mode` forms; weight table with a mute toggle per row (`POST /gate/mute`); existing "Clear all feedback" |

POST routes reuse the `sameOriginFeedback` checks. All responses `Cache-Control: private, no-store`.

### 6.5 Test plan

Following `docs/adr/0002-testing-strategy.md`: named subtests, no loops, expected values from independent sources.

- `internal/rank/testdata/rubric-vectors.json`: one case per demo category (profanity, threat, code, leaked key, hate, useful answer, deep thread, spam, scam) plus false-positive cases: "I'm going to kill it at the gig tonight" (no threat), "Scunthorpe" (no profanity), a message ID and a media digest (no secret), "That's a scam. Never share your recovery phrase." (no solicitation), a 12-word non-BIP-39 sentence, Spanish text (readability null, profanity null). Expected values written by hand.
- `internal/rank/testdata/model-vectors.json`: the 12 votes and 8 posts of section 4 with expected w_f and p to 1e-4, the 0/1/5-vote cold-start rows, and the decay row. These numbers were computed by hand and cross-checked outside Go.
- Gate tests: each row of 3.2, each threshold edge (0.40, 0.25, support 6), language guard, page cap, exploration with a fixed seed, each guarantee in 3.4, thread rules.
- Determinism: the same votes in two different input orders give identical `Weights()`.
- Integration (`internal/server`): vote, then the feed shows the expected placeholder; mode Off shows everything; cookie import.
- Benchmarks: `Score` over 300 posts; `Extract` over the demo set.

### 6.6 Issues

| # | Issue | Owns | Depends on |
| --- | --- | --- | --- |
| 1 | ADR 0003 row for `internal/rank`; `content.Segments` | `docs/adr/0003-architecture.md`, `internal/content/segments.go`, its test | — |
| 2 | Rubric: `Extract`, `Indicators`, `Floor`, embedded lexicons and patterns, emoji table, rubric vectors | `internal/rank/rubric*.go`, `internal/rank/lexicon/*`, `internal/rank/testdata/rubric-vectors.json` | 1 |
| 3 | Model: `Train`, `Score`, `Weights`, model vectors from section 4 | `internal/rank/model*.go`, `internal/rank/testdata/model-vectors.json` | — (indicators are strings) |
| 4 | Feed profile store, profile cookie, vote snapshots, cookie import | `internal/server/profile.go`, `internal/server/feedback.go` | 2 |
| 5 | Gate in feed and thread, placeholders, hidden count and view, reveal and masking | `internal/rank/gate*.go`, `internal/server/client.go`, `internal/server/replies.go`, `internal/server/post.go`, `internal/server/templates/gate.html`, route table | 2, 3, 4 |
| 6 | Settings: mode switch, weight table, mute | `internal/server/settings.go`, `internal/server/templates/settings.html` | 4, 5 |

## 7. Open decisions for the owner

1. **Default ordering.** Recommend reverse-chronological by default, with "Best first" as an opt-in. Scored ordering by default breaks offset pagination and hides how the gate works behind reordering.
2. **Floors on by default, including adult-label collapse.** Recommend On for all rows of 3.2. The reader can switch to Off at any time.
3. **Storage.** Recommend the server-side per-browser profile in the client role's journal (6.3). Cookies cannot hold the history; browser storage would need custom JavaScript, which `AGENTS.md` rules out.
4. **How aggressive taste may be.** Recommend show ≥ 0.40, collapse 0.25–0.40, hide < 0.25 with support ≥ 6. A more cautious alternative: taste only ever collapses, and only floors hide.
5. **Lexicons.** Recommend small curated English lists written for this project, versioned with the rubric, with other languages returning null until lists exist. Third-party lists are larger but bring licence review and their own biases.
6. **Secret display.** Recommend masking matched spans on "Show anyway", with a second click for the exact text. The alternative is to show the text verbatim, which is simpler and helps copying.
