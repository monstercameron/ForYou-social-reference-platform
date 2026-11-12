package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type seedAuthor struct {
	label string
	key   ed25519.PrivateKey
}

type plannedPost struct {
	key, author, kind, batch string
	network                  string
	draft                    draft
	parentKey                string
	replyKey, supersedesKey  string
	refTargetKey             string
	name                     *plannedName
}

type plannedName struct {
	displayName string
	avatar      string
}

type draft struct {
	format, text string
	media        []wire.MediaReference
	refs         []wire.PostReference
	replyTo      string
	supersedes   string
}

func authors() []seedAuthor {
	result := make([]seedAuthor, 3)
	for i := range result {
		label := fmt.Sprintf("seed-author-%d", i+1)
		result[i] = seedAuthor{label: label, key: keyForLabel(label)}
	}
	return result
}

func demoAuthors() []seedAuthor {
	labels := []string{"seed-reply-mint", "seed-reply-blue", "seed-reply-lilac", "seed-reply-peach", "seed-signer-1", "seed-signer-2", "seed-signer-3", "seed-signer-4", "seed-signer-5", "seed-signer-6", "seed-poster-1", "seed-poster-2", "seed-poster-3", "seed-poster-4"}
	result := make([]seedAuthor, 0, len(labels))
	for _, label := range labels {
		result = append(result, seedAuthor{label: label, key: keyForLabel(label)})
	}
	return result
}

func keyForLabel(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	key, err := keys.NewKeyFromSeed(seed[:])
	if err != nil {
		panic(err)
	}
	return key
}

func allAuthors() []seedAuthor {
	return append(authors(), demoAuthors()...)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mediaSlot(id, filename, kind, alt, base string, data []byte) wire.MediaReference {
	var altText *string
	if alt != "" {
		altText = &alt
	}
	return wire.MediaReference{ID: id, URL: strings.TrimRight(base, "/") + "/" + filename, Kind: kind, SHA256: digest(data), SizeBytes: int64(len(data)), AltText: altText}
}

func showcasePlan(media map[string][]byte, base string) []plannedPost {
	a := authors()
	landscape := func(n int) wire.MediaReference {
		name := fmt.Sprintf("landscape-%d.png", n)
		return mediaSlot(fmt.Sprintf("landscape-%d", n), name, "image", fmt.Sprintf("Landscape %d", n), base, media[name])
	}
	flower := mediaSlot("flower", "flower.mp4", "video", "", base, media["flower.mp4"])
	land := []wire.MediaReference{landscape(1), landscape(2), landscape(3)}
	first := "Hello from the ForYou demo network."
	md := "A short **Markdown** example with `code`, a [plain link](https://example.com) and literal HTML: <b>still text</b>."
	posts := []plannedPost{
		{key: "showcase-1", author: a[0].label, kind: "post", batch: "A", network: wire.BitcoinMainnet, draft: draft{format: "text/plain", text: first}},
		{key: "showcase-2", author: a[1].label, kind: "post", batch: "A", network: wire.BitcoinMainnet, draft: draft{format: "text/markdown", text: md}},
		{key: "showcase-3", author: a[0].label, kind: "media", batch: "A", network: wire.BitcoinMainnet, draft: draft{format: "text/markdown", text: "Three views:\n\n![Landscape 1](fy-media:landscape-1)\n\n![Landscape 2](fy-media:landscape-2)\n\n![Landscape 3](fy-media:landscape-3)", media: land}},
		{key: "showcase-4", author: a[2].label, kind: "media", batch: "A", network: wire.BitcoinMainnet, draft: draft{format: "text/plain", text: "A demo video is available in the media slot.", media: []wire.MediaReference{flower}}},
		{key: "showcase-5", author: a[1].label, kind: "reply", batch: "A", network: wire.BitcoinMainnet, replyKey: "showcase-1", draft: draft{format: "text/plain", text: "A friendly reply."}},
		{key: "showcase-6", author: a[2].label, kind: "reply", batch: "A", network: wire.BitcoinMainnet, replyKey: "showcase-5", draft: draft{format: "text/plain", text: "A reply to the earlier reply."}},
		{key: "showcase-7", author: a[2].label, kind: "refs", batch: "A", network: wire.BitcoinMainnet, refTargetKey: "showcase-2", draft: draft{format: "text/markdown", text: "See [the second post](fy-ref:post-two) and [author one](fy-ref:author-one).", refs: []wire.PostReference{{ID: "post-two", Target: ""}, {ID: "author-one", Target: keys.DIDOf(a[0].key)}}}},
		{key: "showcase-8", author: a[0].label, kind: "edit", batch: "A", network: wire.BitcoinMainnet, supersedesKey: "showcase-1", draft: draft{format: "text/plain", text: first + " Edited for clarity."}},
		{key: "showcase-9", author: a[0].label, kind: "edit", batch: "B", network: wire.BitcoinMainnet, supersedesKey: "showcase-8", draft: draft{format: "text/plain", text: first + " Second edition."}},
		{key: "showcase-10", author: a[1].label, kind: "reply", batch: "B", network: wire.BitcoinMainnet, replyKey: "showcase-9", draft: draft{format: "text/plain", text: "Replying to the newest version."}},
		{key: "showcase-11", author: a[2].label, kind: "post", batch: "B", network: wire.EthereumMainnet, draft: draft{format: "text/plain", text: "Ethereum-funded demo post."}},
		{key: "showcase-12", author: a[1].label, kind: "post", batch: "B", network: wire.BitcoinMainnet, draft: draft{format: "text/plain", text: strings.Repeat("A deliberately longer demo post. ", 45)}},
	}
	return posts
}

func resolvePlanIDs(item plannedPost, ids map[string]string) plannedPost {
	if item.replyKey != "" {
		item.draft.replyTo = ids[item.replyKey]
	}
	if item.supersedesKey != "" {
		item.draft.supersedes = ids[item.supersedesKey]
	}
	if item.refTargetKey != "" {
		item.draft.refs[0].Target = ids[item.refTargetKey]
	}
	return item
}

func validateMediaBase(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid media base URL")
	}
	for _, file := range []string{"landscape-1.png", "landscape-2.png", "landscape-3.png", "flower.mp4", "avatar-1.png", "avatar-2.png", "avatar-3.png"} {
		if _, err := content.ParseExternalURL(strings.TrimRight(raw, "/") + "/" + file); err != nil {
			return fmt.Errorf("invalid media URL for %s: %w", file, err)
		}
	}
	return nil
}

func demoPlan(media map[string][]byte, base string) []plannedPost {
	posts := make([]plannedPost, 0, 322)
	signers := []plannedName{{displayName: "Elliot Brooks", avatar: "avatar-1.png"}, {displayName: "Maya Chen", avatar: "avatar-2.png"}, {displayName: "Noor Williams"}, {displayName: "Sofia Park", avatar: "avatar-3.png"}, {displayName: "Maya Chen", avatar: "avatar-2.png"}, {displayName: "Avery Stone"}}
	seenSigner := make(map[int]bool)
	for i, mock := range demoPosts {
		item := plannedPost{key: mock.id, author: fmt.Sprintf("seed-poster-%d", i%4+1), kind: "post", batch: "A", network: wire.BitcoinMainnet, draft: draft{format: "text/plain", text: mock.text}}
		if mock.kind == "images" || mock.kind == "video" {
			item.kind = "media"
		}
		if i%3 == 1 {
			signer := (i / 3) % len(signers)
			item.author = fmt.Sprintf("seed-signer-%d", signer+1)
			if !seenSigner[signer] {
				item.name = &signers[signer]
				seenSigner[signer] = true
			}
		}
		if mock.kind == "images" {
			item.draft.media = []wire.MediaReference{mediaSlot("landscape-1", "landscape-1.png", "image", "", base, media["landscape-1.png"]), mediaSlot("landscape-2", "landscape-2.png", "image", "", base, media["landscape-2.png"]), mediaSlot("landscape-3", "landscape-3.png", "image", "", base, media["landscape-3.png"])}
		}
		if mock.kind == "video" {
			item.draft.media = []wire.MediaReference{mediaSlot("flower", "flower.mp4", "video", "", base, media["flower.mp4"])}
		}
		posts = append(posts, item)
		appendDemoReplies(&posts, mock.id, mock.id, demoReplies(mock.id), media, base, new(int))
	}
	return posts
}

func appendDemoReplies(posts *[]plannedPost, root, parent string, replies []reply, media map[string][]byte, base string, sequence *int) {
	for _, fixture := range replies {
		*sequence++
		key := fmt.Sprintf("%s-reply-%03d", root, *sequence)
		item := plannedPost{key: key, author: "seed-reply-" + fixture.Color, kind: "reply", batch: "A", network: wire.BitcoinMainnet, parentKey: parent, draft: draft{format: "text/markdown", text: fixture.Text}}
		for n := 1; n <= 3; n++ {
			slotID := fmt.Sprintf("landscape-%d", n)
			marker := "fy-media:" + slotID
			if markerAt := strings.Index(fixture.Text, marker); markerAt >= 0 {
				start := strings.LastIndex(fixture.Text[:markerAt], "![") + 2
				end := strings.Index(fixture.Text[start:], "](")
				alt := slotID
				if start >= 2 && end >= 0 {
					alt = fixture.Text[start : start+end]
				}
				item.draft.media = append(item.draft.media, mediaSlot(slotID, slotID+".png", "image", alt, base, media[slotID+".png"]))
			}
		}
		*posts = append(*posts, item)
		appendDemoReplies(posts, root, key, fixture.Children, media, base, sequence)
	}
}

func countDemoPosts(plan []plannedPost) int {
	count := 0
	for _, item := range plan {
		if item.kind != "reply" {
			count++
		}
	}
	return count
}

func countDemoReplies(plan []plannedPost) int {
	count := 0
	for _, item := range plan {
		if item.kind == "reply" {
			count++
		}
	}
	return count
}

func repliesFor(plan []plannedPost, root string) int {
	count := 0
	for _, item := range plan {
		if item.parentKey != "" && strings.HasPrefix(item.key, root+"-reply-") {
			count++
		}
	}
	return count
}

func deepestDemoThread(plan []plannedPost) int {
	depths := make(map[string]int, len(plan))
	maximum := 0
	for _, item := range plan {
		if item.parentKey == "" {
			depths[item.key] = 0
			continue
		}
		depths[item.key] = depths[item.parentKey] + 1
		if depths[item.key] > maximum {
			maximum = depths[item.key]
		}
	}
	return maximum
}

func signerPostCount(plan []plannedPost) int {
	count := 0
	for _, item := range plan {
		if strings.HasPrefix(item.author, "seed-signer-") {
			count++
		}
	}
	return count
}

func sharedMayaSigners(plan []plannedPost) bool {
	first, second := "", ""
	for _, item := range plan {
		if item.name != nil && item.name.displayName == "Maya Chen" {
			if first == "" {
				first = item.author
			} else if item.author != first {
				second = item.author
			}
		}
	}
	return first != "" && second != "" && first != second
}

func findPlanItem(plan []plannedPost, key string) *plannedPost {
	for i := range plan {
		if plan[i].key == key {
			return &plan[i]
		}
	}
	return nil
}
