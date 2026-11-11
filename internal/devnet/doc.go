// Package devnet builds a deterministic development trust root. Dev keys are
// public knowledge and worthless. The genesis certificate is exempt from the
// 30-day notice, valid for the maximum 366 days, and covers signed times from
// 2026-07-01 up to (but not including) 2027-07-02. Each approval is 92 days before
// activation, meeting the 60-day notice. Schedule 1 has been in force since
// 2026-10-01 and remains in force after expiry until a successor takes effect.
// Policy 1 is approved 92 days before its 2026-10-01 UTC quarter start, meeting
// the same notice. Every release-signed object uses the unchanged genesis set,
// so no prior-object carry-over is needed. Renewals keep the network valid:
// renewal k starts 365 days after genesis, is issued 30 days earlier, and ends
// 366 days after its start. Consecutive certificates overlap by one day, and
// Certificates returns each renewal once its issue time has arrived.
package devnet
