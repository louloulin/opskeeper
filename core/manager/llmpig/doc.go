// Package llmpig is the control plane's PiG-backed implementation of
// llm.Client, and the only place outside core/pig that may name a type from
// the PiG adapter.
//
// Why it is its own package rather than three files inside core/manager/pkg/llm:
//
//   - core/manager/pkg is the shared floor. It holds the LLM port every
//     bounded context speaks, the provider table, the router, and the budget
//     hook, and none of those need to know which runtime answers a request.
//     The moment pkg/llm imports core/pig, a PiG upgrade stops being a change
//     to core/pig: it reaches into the floor and out to every context that
//     imports it. That is the promise decisions 57 and 62 make, and this
//     package is where it stops being a promise and becomes a directory.
//   - The three files that moved here (pigclient.go, pigregistry.go,
//     pigsettings.go) are cohesive and share nothing with the floor except
//     its exported vocabulary. Splitting them by *reason to change* rather
//     than by *what they call* is the whole move.
//
// The dependency now points the right way: llmpig imports the floor, never
// the reverse. Composition happens in cmd/opskeeper, which is the one place
// allowed to know both.
//
// What is deliberately NOT here: the ports.Chat contract, the provider
// table, the router, the budget checker, and the HTTP wire format. Those
// stay in the floor, and llmpig is a second implementation behind the same
// interface — not a replacement for it. A deployment with no PiG in the
// picture still builds and still runs.
package llmpig
