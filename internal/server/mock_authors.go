package server

import (
	"bytes"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
)

// Two demo keys sign the same name on purpose: a name is a label, the key is the identity.
var demoSigners = []struct {
	name string
	seed byte
}{{"Elliot Brooks", 1}, {"Maya Chen", 2}, {"Noor Williams", 3}, {"Sofia Park", 4}, {"Maya Chen", 5}, {"Avery Stone", 6}}

var demoNamesIssuedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func demoNameRecords() []names.NameRecord {
	records := make([]names.NameRecord, len(demoSigners))
	for i, s := range demoSigners {
		key, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{s.seed}, 32))
		if err != nil {
			panic(err)
		}
		name := s.name
		if records[i], err = names.Build(key, &name, demoNamesIssuedAt, nil); err != nil {
			panic(err)
		}
	}
	return records
}

// signDemoPosts gives every third post a signed display name.
func signDemoPosts(posts []post) {
	records := demoNameRecords()
	for i := 1; i < len(posts); i += 3 {
		r := records[i/3%len(records)]
		posts[i].SignedName, posts[i].AuthorID, posts[i].Fingerprint = *r.DisplayName, r.AuthorID, names.Fingerprint(r.AuthorID)
	}
}
