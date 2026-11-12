package main

import (
	"strings"
	"testing"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

func TestShowcasePlan(t *testing.T) {
	t.Run("fixed prior IDs resolve versions replies and references", func(t *testing.T) {
		id := func(n string) string { return "fy1:" + strings.Repeat(n, 64) }
		plan := showcasePlan(map[string][]byte{"landscape-1.png": []byte("one"), "landscape-2.png": []byte("two"), "landscape-3.png": []byte("three"), "flower.mp4": []byte("four")}, "https://example.com/media")
		ids := map[string]string{"showcase-1": id("1"), "showcase-2": id("2"), "showcase-5": id("5"), "showcase-8": id("8"), "showcase-9": id("9")}
		plan[4] = resolvePlanIDs(plan[4], ids)
		plan[5] = resolvePlanIDs(plan[5], ids)
		plan[6] = resolvePlanIDs(plan[6], ids)
		plan[7] = resolvePlanIDs(plan[7], ids)
		plan[8] = resolvePlanIDs(plan[8], ids)
		plan[9] = resolvePlanIDs(plan[9], ids)
		if len(plan) != 12 || len(plan[:8]) != 8 {
			t.Fatalf("plan size = %d; want 12 with 8 in batch A", len(plan))
		}
		if got := len(plan[2].draft.media); got != 3 {
			t.Fatalf("post 3 slots = %d; want 3", got)
		}
		if strings.Count(plan[2].draft.text, "fy-media:landscape-1") != 1 || strings.Count(plan[2].draft.text, "fy-media:landscape-2") != 1 || strings.Count(plan[2].draft.text, "fy-media:landscape-3") != 1 {
			t.Fatal("post 3 does not bind each landscape slot exactly once")
		}
		if plan[7].draft.supersedes != id("1") || plan[8].draft.supersedes != id("8") || plan[8].draft.replyTo != "" {
			t.Fatalf("version chain = %#v, %#v", plan[7].draft, plan[8].draft)
		}
		if plan[9].draft.replyTo != id("9") || plan[5].draft.replyTo != id("5") {
			t.Fatalf("reply targets = %q and %q", plan[9].draft.replyTo, plan[5].draft.replyTo)
		}
		if plan[6].draft.refs[0].Target != id("2") || plan[6].draft.refs[1].Target != keys.DIDOf(authors()[0].key) {
			t.Fatalf("reference targets = %#v", plan[6].draft.refs)
		}
		postIntent := intentFor(authors()[0].key, "2026-11-11T12:00:00Z", plan[0].draft)
		replyIntent := intentFor(authors()[1].key, "2026-11-11T12:00:00Z", plan[4].draft)
		editIntent := intentFor(authors()[0].key, "2026-11-11T12:00:00Z", plan[7].draft)
		if err := message.ValidateIntent(postIntent); err != nil {
			t.Fatal(err)
		}
		if err := message.ValidateIntent(replyIntent); err != nil {
			t.Fatal(err)
		}
		if err := message.ValidateIntent(editIntent); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDemoPlan(t *testing.T) {
	plan := demoPlan(map[string][]byte{"landscape-1.png": []byte("one"), "landscape-2.png": []byte("two"), "landscape-3.png": []byte("three"), "flower.mp4": []byte("four"), "avatar-1.png": []byte("a"), "avatar-2.png": []byte("b"), "avatar-3.png": []byte("c")}, "https://example.com/media")
	t.Run("posts and replies match fixtures", func(t *testing.T) {
		if got := countDemoPosts(plan); got != 53 {
			t.Fatalf("posts = %d; want 53", got)
		}
		if got := countDemoReplies(plan); got != 269 {
			t.Fatalf("replies = %d; want 269", got)
		}
		if got := repliesFor(plan, "weather"); got != 0 {
			t.Fatalf("weather replies = %d; want 0", got)
		}
		if got := repliesFor(plan, "poetry"); got != 0 {
			t.Fatalf("poetry replies = %d; want 0", got)
		}
		if got := deepestDemoThread(plan); got != 8 {
			t.Fatalf("deepest reply depth = %d; want 8", got)
		}
	})
	t.Run("signed names and sensitive fixture text are retained", func(t *testing.T) {
		if got := signerPostCount(plan); got != 18 {
			t.Fatalf("signed-name posts = %d; want 18", got)
		}
		if !sharedMayaSigners(plan) {
			t.Fatal("expected two distinct signer keys named Maya Chen")
		}
		if !demoFixtureContains("open-source", "fy_demo_4f9c2b7e1a6d4c0f9b3e8a5d7c1f2e6b") {
			t.Fatal("documented fixture token missing")
		}
		if !demoFixtureContains("tech-video", "AKIAIOSFODNN7EXAMPLE") {
			t.Fatal("example access key missing")
		}
	})
	t.Run("travel image reply declares only the bound slot", func(t *testing.T) {
		item := findPlanItem(plan, "travel-reply-001")
		if item == nil || len(item.draft.media) != 1 || item.draft.media[0].ID != "landscape-1" || item.draft.media[0].AltText == nil || *item.draft.media[0].AltText != "harbor hills at dusk" {
			t.Fatalf("travel media binding = %#v", item)
		}
	})
	t.Run("representative intents validate", func(t *testing.T) {
		post := intentFor(demoAuthors()[10].key, "2026-11-11T12:00:00Z", findPlanItem(plan, "maya").draft)
		reply := intentFor(demoAuthors()[0].key, "2026-11-11T12:00:00Z", findPlanItem(plan, "open-source-reply-001").draft)
		edit := intentFor(authors()[0].key, "2026-11-11T12:00:00Z", showcasePlan(map[string][]byte{}, "https://example.com")[7].draft)
		if err := message.ValidateIntent(post); err != nil {
			t.Fatal(err)
		}
		if err := message.ValidateIntent(reply); err != nil {
			t.Fatal(err)
		}
		if err := message.ValidateIntent(edit); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDigest(t *testing.T) {
	t.Run("FIPS SHA-256 abc vector", func(t *testing.T) {
		if got, want := digest([]byte("abc")), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; got != want {
			t.Fatalf("digest(abc) = %s; want %s", got, want)
		}
	})
}

func intentFor(key []byte, stamp string, d draft) wire.Intent {
	privateKey := key
	payload := wire.PostPayload{ContentFormat: d.format, Text: d.text}
	if len(d.media) > 0 {
		payload.MediaSlots = &d.media
	}
	if len(d.refs) > 0 {
		payload.Refs = &d.refs
	}
	payload.ReplyTo, payload.Supersedes = d.replyTo, d.supersedes
	return wire.Intent{AuthorID: keys.DIDOf(privateKey), Timestamp: stamp, MessageType: "post", Version: wire.Version{Major: 2}, Payload: payload}
}
