// Package sdk provides the Go client every ForYou consumer uses. A response is
// never trusted until the verification functions of this package accept it.
// The faux chain is consumed through internal/faux's Go client, not this HTTP
// client.
package sdk
