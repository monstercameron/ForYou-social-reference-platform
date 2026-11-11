package server

import (
	"crypto/sha256"
	"fmt"
)

type post struct {
	ID, Name, Handle, Initials, Color, Time, Text, Topic, Art string
	Receipt, Kind, Feedback                                   string
	// SignedName is a display name the author key signed; AuthorID is that key.
	// Avatar is the picture that goes with the name, when the author set one.
	SignedName, AuthorID, Fingerprint, Avatar string
	// Why says why this reader is shown the post, where a view chose it for them.
	Why                string
	ReplyCount         int
	IsReply, Following bool
}

func mockReceipt(seed string) string {
	sum := sha256.Sum256([]byte("foryou-demo:" + seed))
	return fmt.Sprintf("%x…%x", sum[:4], sum[30:])
}
