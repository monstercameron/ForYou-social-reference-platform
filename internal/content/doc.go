// Package content validates post payloads and prepares content for safe display.
// It defines CommonMark without extensions, link, reference and media plans,
// restricted external URL rules, fetch destination policy, media verification,
// and safe HTML output. Validation, parsing and rendering perform no network I/O;
// only the explicit fetch client may access the network.
package content
