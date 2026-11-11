package server

import (
	"bytes"
	"strconv"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
)

// Two demo keys use the same name and the same picture on purpose: a name and
// an avatar are labels, the key is the identity. The pictures are bundled demo
// files; avatar 0 means the author set none.
var demoSigners = []struct {
	name   string
	seed   byte
	avatar int
}{{"Elliot Brooks", 1, 1}, {"Maya Chen", 2, 2}, {"Noor Williams", 3, 0}, {"Sofia Park", 4, 3}, {"Maya Chen", 5, 2}, {"Avery Stone", 6, 0}}

var demoNamesIssuedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func demoNameRecords() []names.NameRecord {
	records := make([]names.NameRecord, len(demoSigners))
	for i, s := range demoSigners {
		key, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{s.seed}, 32))
		if err != nil {
			panic(err)
		}
		name := s.name
		if records[i], err = names.Build(key, &name, nil, demoNamesIssuedAt, nil); err != nil {
			panic(err)
		}
	}
	return records
}

// signDemoPosts gives every third post a signed display name.
func signDemoPosts(posts []post) {
	records := demoNameRecords()
	for i := 1; i < len(posts); i += 3 {
		signer := i / 3 % len(records)
		r := records[signer]
		posts[i].SignedName, posts[i].AuthorID, posts[i].Fingerprint = *r.DisplayName, r.AuthorID, names.Fingerprint(r.AuthorID)
		if picture := demoSigners[signer].avatar; picture != 0 {
			posts[i].Avatar = "/static/demo-avatar-" + strconv.Itoa(picture) + ".png"
		}
	}
}
