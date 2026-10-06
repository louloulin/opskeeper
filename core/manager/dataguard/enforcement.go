package dataguard

// This file is the honest answer to a question the vocabulary kept answering
// wrong: what does Data-Guard actually enforce today?
//
// The sensitivity constants and the compliance tag both promise controls in
// their own doc comments — a reader role, an override, two approvers, an
// audit-retention period injected into the sandbox. Those promises were the
// kind that survive for years precisely because they live in comments and in
// a JSON column: nothing reads `enforced: true` and changes its behaviour, and
// nothing reports that it did not. So they are written down here, one row per
// promise, with the status each one actually has today.
//
// The status vocabulary is three words, not two, because "the code exists" and
// "the code runs" are different facts and only two words cannot tell them
// apart:
//
//   - StatusEnforced  the control runs on some path a request can take.
//   - StatusInert     the control is written and wired to nothing. Tests reach
//     it; production does not. This is the state that reads as "implemented"
//     from inside the package and as "absent" from outside it.
//   - StatusDeclared  there is no code at all. The promise lives in a comment
//     and in a persisted flag.
//
// Only StatusEnforced may be described as enforced anywhere else in the tree.
// Everything else is a declaration the console may show and nothing may rely
// on.

// Status is what a promised control actually does today.
type Status string

const (
	// StatusEnforced means the control runs on a path a request can take.
	StatusEnforced Status = "enforced"
	// StatusInert means the control is implemented and wired to nothing:
	// only tests reach it.
	StatusInert Status = "inert"
	// StatusDeclared means the promise has no implementation at all.
	StatusDeclared Status = "declared"
)

// ControlClaim is one promise, and what is true about it today.
//
// Probe is the symbol whose presence or absence in non-test code decides the
// row's status, and it is required for every row except a pure declaration —
// a registry that asserts reachability without naming what it is asserting
// reachability of is a registry of opinions.
type ControlClaim struct {
	// ID is stable and is what the gate and the ledger refer to.
	ID string
	// Claim is the promise, in the words the vocabulary uses.
	Claim string
	// Status is what is true today.
	Status Status
	// Probe is the symbol that proves the status: named for enforced and
	// inert rows, empty for declared ones.
	Probe string
	// Note says what stands in the control's place while it is not enforced,
	// or why nothing does.
	Note string
}

// Claims is the registry. It is a function rather than a package variable so
// a caller cannot mutate the shared slice and hand itself a cleaner report.
func Claims() []ControlClaim {
	return []ControlClaim{
		{
			ID: "compliance.enforced-tag",
			Claim: "a compliance tag with Enforced=true injects hard constraints into the cmdpolicy " +
				"sandbox: encryption-at-rest, audit-log-retention-1y, audit-log-retention-6mo, " +
				"audit-log-retention-6y, mfa-on-write, geo-eu-only, phi-encryption, " +
				"access-control-rbac, incident-response-24h, subject-erasure, purpose-limitation, " +
				"data-minimization, minimum-necessary, change-management, access-review-quarterly " +
				"and logical-access-logging",
			Status: StatusDeclared,
			Note: "nothing reads the flag: the sandbox has no compliance notion, and " +
				"retention and encryption controls have no implementation to inject. The flag " +
				"is a label the console may display and nothing may rely on.",
		},
		{
			ID:     "compliance.control-catalog",
			Claim:  "the five frameworks ship a recommended control list",
			Status: StatusDeclared,
			Note: "the catalogs exist as a Go function and the tag store round-trips whatever " +
				"the console posts, but no request path serves them: a one-click list has no " +
				"route behind it. This row was written as enforced, then as inert (the function " +
				"turned out to be called only by this registry), and the gate caught both — a " +
				"status column is worth having only while something checks it.",
		},
		{
			ID:     "sensitivity.escalates-severity",
			Claim:  "TopSecret and Restricted raise a call to dangerous, Confidential to mutating",
			Status: StatusInert,
			Probe:  "NewPolicyFromConfig",
			Note: "PausePolicyImpl implements the mapping and nothing constructs it in production, " +
				"so a TopSecret resource does not actually escalate on any request path. The gate " +
				"that does run is the single-sign approval gate in the kernel.",
		},
		{
			ID:     "sensitivity.confidential-reader-role",
			Claim:  "Confidential data requires the confidential-reader role",
			Status: StatusInert,
			Probe:  "AllowWithSensitivity",
			Note: "the check exists — Enforcer.AllowWithSensitivity answers RBAC and then the " +
				"sensitivity tier, against a per-user, per-org tier table — and nothing calls it. " +
				"This row was written as declared on the strength of a grep that only looked at " +
				"this package; the control was one module over and implemented all along, which is " +
				"the same blindness in the other direction.",
		},
		{
			ID:     "sensitivity.restricted-reader-role",
			Claim:  "Restricted data requires the restricted-reader role, and writes need an override",
			Status: StatusInert,
			Probe:  "AllowWithSensitivity",
			Note: "the tier half of this promise is the same unwired check as the confidential " +
				"row. The write-override half has no implementation at all: the escalation to a " +
				"dangerous approval exists in PausePolicyImpl, which production never constructs.",
		},
		{
			ID:     "sensitivity.top-secret-dual-approval",
			Claim:  "TopSecret data is readable by nobody and its writes need two approvers",
			Status: StatusInert,
			Probe:  "WithDualSignPolicy",
			Note: "the validator exists and is never injected, and the proposal tables have one " +
				"approved_by column each — there is nowhere to put a second signature. Dual sign " +
				"is the ADR-019 conclusion and it has never fired.",
		},
	}
}

// Claim returns the registry row with this id.
func Claim(id string) (ControlClaim, bool) {
	for _, claim := range Claims() {
		if claim.ID == id {
			return claim, true
		}
	}
	return ControlClaim{}, false
}

// DeclaredControls returns every control name the built-in frameworks mention
// without saying which of them this build enforces.
//
// The list is what an operator reading a label cannot tell apart, and it is
// exported so the gate that guards the registry is comparing against the same
// source the labels come from rather than a copy of it.
func DeclaredControls() []string {
	var out []string
	for _, framework := range AllFrameworks {
		out = append(out, DefaultFrameworkControls()[framework]...)
	}
	return out
}
