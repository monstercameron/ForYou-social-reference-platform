package server

import (
	"crypto/sha256"
	"fmt"
	"html/template"
	"time"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

type post struct {
	ID, Name, Handle, Initials, Color, Time, Text, Topic, Art string
	Receipt, Kind, Feedback                                   string
	// SignedName is a display name the author key signed; AuthorID is that key.
	// Avatar is the picture that goes with the name, when the author set one.
	SignedName, AuthorID, Fingerprint, Avatar string
	// Why says why this reader is shown the post, where a view chose it for them.
	Why string
	// Rating is the rubric rating of the post as this reader sees it.
	Rating             rating
	ReplyCount         int
	IsReply, Following bool
	// Format is the post's content_format; empty means plain text.
	// Timestamp is the signed timestamp of a network post.
	Format, Timestamp string
	// DemoData marks a demo post shown because the configured provider had none.
	DemoData bool
	// Media lists a network post's media slots as inert lines; nothing is fetched.
	Media template.HTML
	// Settlement and Class are the local verification state of a network post;
	// both are empty for demo posts. RecheckBy dates a pending settlement.
	Settlement, Class, RecheckBy string
	// Unverifiable explains why a network post cannot be verified right now.
	Unverifiable string
	// ReplyTo and Supersedes are the signed parent and predecessor message IDs.
	ReplyTo, Supersedes string
	// IssuedAt is the provider receipt time of a network post. RootSigned is true
	// when the author's own key signed it without delegation.
	IssuedAt   time.Time
	RootSigned bool
	// Payload is the signed payload of a network post; its declared references and media slots drive Markdown rendering.
	Payload wire.PostPayload
	// Verified is the full verification state of a network record, with its signed bytes; nil for demo data.
	Verified *sdk.Record
}

func mockReceipt(seed string) string {
	sum := sha256.Sum256([]byte("foryou-demo:" + seed))
	return fmt.Sprintf("%x…%x", sum[:4], sum[30:])
}

// format is the content format the post's text is read in.
func (p post) format() string {
	if p.Format == "" {
		return content.FormatPlain
	}
	return p.Format
}
