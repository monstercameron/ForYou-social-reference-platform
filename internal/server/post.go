package server

import (
	"crypto/sha256"
	"fmt"
)

type post struct {
	ID, Name, Handle, Initials, Color, Time, Text, Topic, Art string
	Receipt, Kind, Feedback                                   string
	// SignedName is a display name the author key signed; AuthorID is that key.
	SignedName, AuthorID, Fingerprint string
	ReplyCount                        int
	IsReply, Following                bool
}

func mockReceipt(seed string) string {
	sum := sha256.Sum256([]byte("foryou-demo:" + seed))
	return fmt.Sprintf("%x…%x", sum[:4], sum[30:])
}
