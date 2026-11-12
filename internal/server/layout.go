package server

// Layout is the shared shell data passed to layout templates. Each template
// folder belongs to one feature. Definitions in feature folders start with
// feed-, explore-, post-, thread-, embed-, share-, compose-, settings-,
// identity-, network-, wallet-, versions-, markdown-, or media-. The layout-
// and card- prefixes are reserved for shared core markup.
type Layout struct {
	Title, Section, Theme string
	Dev                   bool
	// Identity is the reader's short author key; Custody says who holds it.
	Identity, Custody string
	// Network describes the configured services; Source says where posts come from.
	Network           []NetworkLine
	NetworkConfigured bool
	Source            string
	// DefaultList discloses who controls the default provider list and how it was chosen.
	DefaultList   string
	Query, Filter string
	Rightbar      bool
	// Ads are the ads most relevant to this reader, for the right bar.
	Ads []post
}

// NetworkLine describes one line in the local network summary.
type NetworkLine struct {
	Label, Value string
}
