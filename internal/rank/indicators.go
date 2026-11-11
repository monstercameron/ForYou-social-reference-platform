package rank

import "sort"

// FloorAction is the strictest safety-floor outcome for the feed.
type FloorAction int

const (
	// FloorNone means no safety floor applies.
	FloorNone FloorAction = iota
	// FloorCollapse keeps the post behind an explicit reveal action.
	FloorCollapse
	// FloorHide removes the post from the feed view.
	FloorHide
)

// FloorResult is the safety floor of one post in the feed.
type FloorResult struct {
	Action  FloorAction
	Reasons []string
	Secrets []SecretMatch // to mask on "Show anyway"
}

// Indicators returns the sorted, distinct taste indicators for f.
func (f Features) Indicators() []string {
	indicators := make(map[string]struct{})
	indicators["len:"+string(f.Length)] = struct{}{}
	if f.Structured {
		indicators["struct:rich"] = struct{}{}
	}
	if f.Readability != nil && *f.Readability < 0.5 {
		indicators["read:dense"] = struct{}{}
	}
	if f.Images >= 1 {
		indicators["media:image"] = struct{}{}
	}
	if f.Videos >= 1 {
		indicators["media:video"] = struct{}{}
	}
	if f.AltCoverage != nil && *f.AltCoverage < 1 {
		indicators["media:noalt"] = struct{}{}
	}
	if f.ActiveLinks >= 1 {
		indicators["link:any"] = struct{}{}
	}
	if f.ActiveLinks >= 3 {
		indicators["link:many"] = struct{}{}
	}
	if f.CodeBlock {
		indicators["code:block"] = struct{}{}
	} else if f.CodeSpan {
		indicators["code:inline"] = struct{}{}
	}
	if f.Profanity != nil {
		if f.Profanity.Mild >= 1 {
			indicators["prof:mild"] = struct{}{}
		}
		if f.Profanity.Strong >= 1 {
			indicators["prof:strong"] = struct{}{}
		}
	}
	if f.Harass >= 0.5 {
		indicators["harass"] = struct{}{}
	}
	if f.CapsRatio >= 0.7 {
		indicators["spam:caps"] = struct{}{}
	}
	if f.Repetition.Rep3 >= 0.4 || f.Repetition.CharRepeat >= 0.5 {
		indicators["spam:repeat"] = struct{}{}
	}
	if f.BangRuns >= 2 {
		indicators["spam:punct"] = struct{}{}
	}
	if f.Repetition.Emoji >= 6 && f.Repetition.EmojiRatio >= 0.3 {
		indicators["spam:emoji"] = struct{}{}
	}
	if f.PromoHits >= 1 {
		indicators["promo"] = struct{}{}
	}
	if f.AdultLabel {
		indicators["adult"] = struct{}{}
	}
	for _, topic := range f.Topics {
		indicators["topic:"+topic] = struct{}{}
	}
	indicators["lang:"+f.Language] = struct{}{}
	if f.Script != "" && f.Script != "latin" {
		indicators["script:"+f.Script] = struct{}{}
	}
	if f.Named {
		indicators["author:named"] = struct{}{}
	}
	if f.Delegate {
		indicators["author:delegate"] = struct{}{}
	}
	if f.IsReply {
		indicators["thread:reply"] = struct{}{}
	}
	if f.Depth >= 4 {
		indicators["thread:deep"] = struct{}{}
	}
	if f.KnownReplies >= 4 {
		indicators["thread:busy"] = struct{}{}
	}
	if f.SelfReply {
		indicators["thread:self"] = struct{}{}
	}
	indicators["key:"+f.AuthorID] = struct{}{}
	result := make([]string, 0, len(indicators))
	for indicator := range indicators {
		result = append(result, indicator)
	}
	sort.Strings(result)
	return result
}

// Floor returns the fixed safety action and reasons for f in a feed.
func (f Features) Floor() FloorResult {
	result := FloorResult{}
	add := func(action FloorAction, reason string) {
		result.Reasons = append(result.Reasons, reason)
		if action > result.Action {
			result.Action = action
		}
	}
	if f.Threat == 1 {
		add(FloorHide, "threat")
	}
	if f.Hate.Directed {
		add(FloorHide, "hate:directed")
	}
	if f.Hate.Dehumanizing {
		add(FloorHide, "hate:dehumanizing")
	}
	if f.SolicitSecret {
		add(FloorHide, "solicit_secret")
	}
	secretKinds := make(map[string]struct{})
	for _, secret := range f.Secrets {
		secretKinds[secret.Kind] = struct{}{}
	}
	if len(secretKinds) > 0 {
		kinds := make([]string, 0, len(secretKinds))
		for kind := range secretKinds {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			add(FloorCollapse, "secret:"+kind)
		}
		result.Secrets = append([]SecretMatch(nil), f.Secrets...)
	}
	if f.Threat == 0.5 {
		add(FloorCollapse, "threat:indirect")
	}
	if f.Hate.Slurs >= 1 && !f.Hate.Directed {
		add(FloorCollapse, "hate:slur")
	}
	if f.Harass >= 0.5 {
		add(FloorCollapse, "harass")
	}
	if f.AdultLabel {
		add(FloorCollapse, "adult")
	}
	if f.CapsRatio >= 0.7 && (f.Repetition.Rep3 >= 0.4 || f.Repetition.CharRepeat >= 0.5) {
		add(FloorCollapse, "spam:extreme")
	}
	if len(result.Reasons) == 0 {
		return FloorResult{}
	}
	return result
}
