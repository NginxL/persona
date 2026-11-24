// Package iphonectl is the core of operating a paired iPhone for the assistant. It runs on the node, behind the
// built-in program that answers the hub's phone calls, so one tool call is one hub ↔ node round trip: Handle takes a
// look, do or status and answers with the result for the model, the same result as data, a screenshot, and, when a
// step needs the owner first, what to ask them.
//
// What it decides: element numbers that stay put between reads, which steps reached the phone (sent / not_sent /
// unknown), limits on every bridge call, every step and the whole call so nothing waits forever, and which steps
// need the owner's approval (payment, deletion, sending a message, a password) — never approving them itself.
//
// It reaches the phone only through Bridge, the device bridge's operations (docs/extensions/iphone-control.md), and
// depends on nothing but the standard library, so tests run it against a fake.
package iphonectl
