package content

import (
	"errors"
	"strings"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func validTestSlot(id string) wire.MediaReference {
	return wire.MediaReference{ID: id, URL: "https://media.example/photo", Kind: "image", SHA256: strings.Repeat("a", 64)}
}

func validTestRef(id, target string) wire.PostReference {
	return wire.PostReference{ID: id, Target: target}
}

func validTestRefs() []wire.PostReference {
	return []wire.PostReference{
		validTestRef("r0", "fy1:0000000000000000000000000000000000000000000000000000000000000000"),
		validTestRef("r1", "fy1:0000000000000000000000000000000000000000000000000000000000000001"),
		validTestRef("r2", "fy1:0000000000000000000000000000000000000000000000000000000000000002"),
		validTestRef("r3", "fy1:0000000000000000000000000000000000000000000000000000000000000003"),
		validTestRef("r4", "fy1:0000000000000000000000000000000000000000000000000000000000000004"),
		validTestRef("r5", "fy1:0000000000000000000000000000000000000000000000000000000000000005"),
		validTestRef("r6", "fy1:0000000000000000000000000000000000000000000000000000000000000006"),
		validTestRef("r7", "fy1:0000000000000000000000000000000000000000000000000000000000000007"),
		validTestRef("r8", "fy1:0000000000000000000000000000000000000000000000000000000000000008"),
		validTestRef("r9", "fy1:0000000000000000000000000000000000000000000000000000000000000009"),
		validTestRef("r10", "fy1:000000000000000000000000000000000000000000000000000000000000000a"),
		validTestRef("r11", "fy1:000000000000000000000000000000000000000000000000000000000000000b"),
		validTestRef("r12", "fy1:000000000000000000000000000000000000000000000000000000000000000c"),
		validTestRef("r13", "fy1:000000000000000000000000000000000000000000000000000000000000000d"),
		validTestRef("r14", "fy1:000000000000000000000000000000000000000000000000000000000000000e"),
		validTestRef("r15", "fy1:000000000000000000000000000000000000000000000000000000000000000f"),
		validTestRef("r16", "fy1:0000000000000000000000000000000000000000000000000000000000000010"),
		validTestRef("r17", "fy1:0000000000000000000000000000000000000000000000000000000000000011"),
		validTestRef("r18", "fy1:0000000000000000000000000000000000000000000000000000000000000012"),
		validTestRef("r19", "fy1:0000000000000000000000000000000000000000000000000000000000000013"),
		validTestRef("r20", "fy1:0000000000000000000000000000000000000000000000000000000000000014"),
		validTestRef("r21", "fy1:0000000000000000000000000000000000000000000000000000000000000015"),
		validTestRef("r22", "fy1:0000000000000000000000000000000000000000000000000000000000000016"),
		validTestRef("r23", "fy1:0000000000000000000000000000000000000000000000000000000000000017"),
		validTestRef("r24", "fy1:0000000000000000000000000000000000000000000000000000000000000018"),
		validTestRef("r25", "fy1:0000000000000000000000000000000000000000000000000000000000000019"),
		validTestRef("r26", "fy1:000000000000000000000000000000000000000000000000000000000000001a"),
		validTestRef("r27", "fy1:000000000000000000000000000000000000000000000000000000000000001b"),
		validTestRef("r28", "fy1:000000000000000000000000000000000000000000000000000000000000001c"),
		validTestRef("r29", "fy1:000000000000000000000000000000000000000000000000000000000000001d"),
		validTestRef("r30", "fy1:000000000000000000000000000000000000000000000000000000000000001e"),
		validTestRef("r31", "fy1:000000000000000000000000000000000000000000000000000000000000001f"),
	}
}

func TestValidateMediaSlots(t *testing.T) {
	check := func(t *testing.T, slots []wire.MediaReference, wantErr bool) {
		t.Helper()
		err := ValidateMediaSlots(slots)
		if wantErr && !errors.Is(err, ErrInvalidMediaSlot) {
			t.Fatalf("ValidateMediaSlots() error = %v; want ErrInvalidMediaSlot", err)
		}
		if !wantErr && err != nil {
			t.Fatalf("ValidateMediaSlots() error = %v", err)
		}
	}
	t.Run("specification_example_slots", func(t *testing.T) {
		// Literal slot values copied from ../ForYou-Social-Ecosystem-Specification/examples/post-with-media-slots.json.
		slots := []wire.MediaReference{
			{ID: "photo", URL: "https://media.example/sunset.jpg", Kind: "image", SHA256: "03c40629f930724f4724705524fc8c3d878697a8c4a6210ac4df06fcebfd9c44", AltText: validStringPointer("Sunset")},
			{ID: "clip", URL: "https://media.example/clip.mp4", Kind: "video", SHA256: "4280f6bae17c31d302094e79f4c75a55c2c5bccf1067241a9454efed447f3a88", AltText: validStringPointer("Short clip")},
		}
		check(t, slots, false)
	})
	t.Run("maximum_slots", func(t *testing.T) {
		slots := []wire.MediaReference{
			validTestSlot("s0"), validTestSlot("s1"), validTestSlot("s2"), validTestSlot("s3"),
			validTestSlot("s4"), validTestSlot("s5"), validTestSlot("s6"), validTestSlot("s7"),
			validTestSlot("s8"), validTestSlot("s9"), validTestSlot("s10"), validTestSlot("s11"),
			validTestSlot("s12"), validTestSlot("s13"), validTestSlot("s14"), validTestSlot("s15"),
		}
		check(t, slots, false)
	})
	t.Run("too_many_slots", func(t *testing.T) {
		err := ValidateMediaSlots([]wire.MediaReference{
			validTestSlot("s0"), validTestSlot("s1"), validTestSlot("s2"), validTestSlot("s3"),
			validTestSlot("s4"), validTestSlot("s5"), validTestSlot("s6"), validTestSlot("s7"),
			validTestSlot("s8"), validTestSlot("s9"), validTestSlot("s10"), validTestSlot("s11"),
			validTestSlot("s12"), validTestSlot("s13"), validTestSlot("s14"), validTestSlot("s15"),
			validTestSlot("s16"),
		})
		if !errors.Is(err, ErrInvalidMediaSlot) || !strings.Contains(err.Error(), "17 slots exceed maximum of 16") {
			t.Fatalf("ValidateMediaSlots() error = %v; want count-limit error wrapping ErrInvalidMediaSlot", err)
		}
	})
	t.Run("duplicate_id", func(t *testing.T) {
		first := validTestSlot("photo")
		second := validTestSlot("photo")
		second.URL = "https://media.example/different"
		check(t, []wire.MediaReference{first, second}, true)
	})
	t.Run("invalid_id", func(t *testing.T) {
		check(t, []wire.MediaReference{validTestSlot("Invalid space")}, true)
	})
	t.Run("short_digest", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.SHA256 = strings.Repeat("a", 63)
		check(t, []wire.MediaReference{slot}, true)
	})
	t.Run("long_digest", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.SHA256 = strings.Repeat("a", 65)
		check(t, []wire.MediaReference{slot}, true)
	})
	t.Run("uppercase_digest", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.SHA256 = strings.Repeat("A", 64)
		check(t, []wire.MediaReference{slot}, true)
	})
	t.Run("nonhex_digest", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.SHA256 = strings.Repeat("g", 64)
		check(t, []wire.MediaReference{slot}, true)
	})
	t.Run("unsupported_kind", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.Kind = "audio"
		check(t, []wire.MediaReference{slot}, true)
	})
	t.Run("minimum_size", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.SizeBytes = 1
		check(t, []wire.MediaReference{slot}, false)
	})
	t.Run("maximum_safe_integer_size", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.SizeBytes = 9007199254740991
		check(t, []wire.MediaReference{slot}, false)
	})
	t.Run("size_above_safe_integer", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.SizeBytes = 9007199254740992
		check(t, []wire.MediaReference{slot}, true)
	})
	t.Run("negative_size", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.SizeBytes = -1
		check(t, []wire.MediaReference{slot}, true)
	})
	t.Run("multibyte_alt_text_boundary", func(t *testing.T) {
		slot := validTestSlot("photo")
		alt := strings.Repeat("é", MaxAltTextRunes)
		slot.AltText = &alt
		check(t, []wire.MediaReference{slot}, false)
	})
	t.Run("alt_text_over_limit", func(t *testing.T) {
		slot := validTestSlot("photo")
		alt := strings.Repeat("é", MaxAltTextRunes+1)
		slot.AltText = &alt
		check(t, []wire.MediaReference{slot}, true)
	})
	t.Run("invalid_media_url", func(t *testing.T) {
		slot := validTestSlot("photo")
		slot.URL = "https://user@media.example/x"
		err := ValidateMediaSlots([]wire.MediaReference{slot})
		if !errors.Is(err, ErrInvalidMediaSlot) || !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("ValidateMediaSlots() error = %v; want ErrInvalidMediaSlot and ErrInvalidURL", err)
		}
	})
}

func TestValidateRefs(t *testing.T) {
	check := func(t *testing.T, refs []wire.PostReference, wantErr bool) {
		t.Helper()
		err := ValidateRefs(refs)
		if wantErr && !errors.Is(err, ErrInvalidRef) {
			t.Fatalf("ValidateRefs() error = %v; want ErrInvalidRef", err)
		}
		if !wantErr && err != nil {
			t.Fatalf("ValidateRefs() error = %v", err)
		}
	}
	t.Run("specification_example_refs", func(t *testing.T) {
		// Literal ref values copied from ../ForYou-Social-Ecosystem-Specification/examples/post-with-refs.json.
		refs := []wire.PostReference{
			validTestRef("r1", "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG"),
			validTestRef("r2", "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"),
		}
		check(t, refs, false)
	})
	t.Run("maximum_refs", func(t *testing.T) {
		check(t, validTestRefs(), false)
	})
	t.Run("too_many_refs", func(t *testing.T) {
		refs := append(validTestRefs(), validTestRef("r32", "fy1:0000000000000000000000000000000000000000000000000000000000000020"))
		err := ValidateRefs(refs)
		if !errors.Is(err, ErrInvalidRef) || !strings.Contains(err.Error(), "33 refs exceed maximum of 32") {
			t.Fatalf("ValidateRefs() error = %v; want count-limit error wrapping ErrInvalidRef", err)
		}
	})
	t.Run("duplicate_id", func(t *testing.T) {
		check(t, []wire.PostReference{
			validTestRef("r1", "fy1:0000000000000000000000000000000000000000000000000000000000000001"),
			validTestRef("r1", "fy1:0000000000000000000000000000000000000000000000000000000000000002"),
		}, true)
	})
	t.Run("duplicate_target", func(t *testing.T) {
		target := "fy1:0000000000000000000000000000000000000000000000000000000000000001"
		check(t, []wire.PostReference{validTestRef("r1", target), validTestRef("r2", target)}, true)
	})
	t.Run("uppercase_id", func(t *testing.T) {
		check(t, []wire.PostReference{validTestRef("UPPER", "fy1:0000000000000000000000000000000000000000000000000000000000000001")}, true)
	})
	t.Run("punctuation_id", func(t *testing.T) {
		check(t, []wire.PostReference{validTestRef("Bad!", "fy1:0000000000000000000000000000000000000000000000000000000000000001")}, true)
	})
	t.Run("did_key_fragment", func(t *testing.T) {
		check(t, []wire.PostReference{validTestRef("r1", "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd#fragment")}, true)
	})
	t.Run("short_did_key", func(t *testing.T) {
		check(t, []wire.PostReference{validTestRef("r1", "did:key:z6Mk1")}, true)
	})
	t.Run("uppercase_message_digest", func(t *testing.T) {
		check(t, []wire.PostReference{validTestRef("r1", "fy1:"+strings.Repeat("A", 64))}, true)
	})
	t.Run("external_target", func(t *testing.T) {
		check(t, []wire.PostReference{validTestRef("r1", "https://unapproved.example")}, true)
	})
}

func TestValidatePostContent(t *testing.T) {
	check := func(t *testing.T, payload wire.PostPayload, wantErr error) {
		t.Helper()
		err := ValidatePostContent(payload)
		if wantErr != nil && !errors.Is(err, wantErr) {
			t.Fatalf("ValidatePostContent() error = %v; want errors.Is(_, %v)", err, wantErr)
		}
		if wantErr == nil && err != nil {
			t.Fatalf("ValidatePostContent() error = %v", err)
		}
	}
	t.Run("plain_text", func(t *testing.T) {
		check(t, wire.PostPayload{ContentFormat: "text/plain", Text: "hello"}, nil)
	})
	t.Run("markdown_with_refs", func(t *testing.T) {
		refs := []wire.PostReference{validTestRef("r1", "fy1:0000000000000000000000000000000000000000000000000000000000000001")}
		check(t, wire.PostPayload{ContentFormat: "text/markdown", Text: "hello", Refs: &refs}, nil)
	})
	t.Run("reference_id_shares_namespace_with_media_id", func(t *testing.T) {
		slots := []wire.MediaReference{validTestSlot("same")}
		refs := []wire.PostReference{validTestRef("same", "fy1:0000000000000000000000000000000000000000000000000000000000000001")}
		check(t, wire.PostPayload{ContentFormat: "text/plain", Text: "", MediaSlots: &slots, Refs: &refs}, nil)
	})
	t.Run("unsupported_format", func(t *testing.T) {
		check(t, wire.PostPayload{ContentFormat: "text/html", Text: "hello"}, ErrInvalidPostContent)
	})
	t.Run("empty_text_without_media", func(t *testing.T) {
		check(t, wire.PostPayload{ContentFormat: "text/plain", Text: ""}, ErrInvalidPostContent)
	})
	t.Run("empty_text_with_media", func(t *testing.T) {
		slots := []wire.MediaReference{validTestSlot("photo")}
		check(t, wire.PostPayload{ContentFormat: "text/plain", Text: "", MediaSlots: &slots}, nil)
	})
	t.Run("slot_error_is_preserved", func(t *testing.T) {
		slots := []wire.MediaReference{validTestSlot("Invalid")}
		check(t, wire.PostPayload{ContentFormat: "text/plain", Text: "hello", MediaSlots: &slots}, ErrInvalidMediaSlot)
	})
}

func validStringPointer(value string) *string { return &value }
