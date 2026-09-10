package cmdpolicy

// policy_pi.go defines DefaultPiCapable — the policy used when edge
// runs with OPSKEEPER_PI_ENABLED=true. It is DefaultReadOnly plus
// minor relaxations needed by the Pi side:
//
//   1. Wider PathAllowlist covering diagnostic locations edge itself
//      must reach (systemd units, journal, machine-id, ...). Pi reads
//      these via the host_files skill which is PathValidator-gated;
//      the broader allowlist is purely a host-side permission change,
//      not a new tool surface.
//
//   2. NetworkHostAllowlist seeded with loopback-only (127.0.0.0/8,
//      ::1/128) so `curl http://127.0.0.1:9101/health` self-checks
//      work without exposing the host to arbitrary outbound.
//
//   3. Extra read-only bins that Pi legitimately needs and that
//      were deliberately omitted from DefaultReadOnly to keep the
//      baseline tight: `kill -l` (list signals), `pgrep`, `pkill
//      --list` etc. Each is a narrow allow — `pkill` killing a
//      process still requires the approval token (enforced by
//      Sandbox via ApprovalChecker; see approval.go).
//
// The "double-sign" enforcement (every mutating action requires a
// reviewer-issued token) is orthogonal to the policy: it's a Sandbox
// runtime check, not a Policy compile-time rule. Mixing the two
// would couple rule-set with request state and break YAML overrides
// that an operator might want to apply.

// DefaultPiCapable returns the Pi-mode policy. It is the production
// policy when OPSKEEPER_PI_ENABLED=true; otherwise DefaultReadOnly()
// is used (see cmdpolicy.DefaultReadOnly). Calling this multiple
// times returns independent policies — mutating one does not affect
// other callers.
func DefaultPiCapable() *Policy {
	// DefaultReadOnly already constructs a fresh Policy each call,
	// so we can mutate fields directly without disturbing other
	// callers. addBin / discoverBin are the only writes; both safe
	// because the returned Policy is the only reference.
	p := DefaultReadOnly()

	// ----- wider PathAllowlist -----
	p.PathAllowlist = append(p.PathAllowlist,
		"/etc/systemd",
		"/run/systemd",
		"/var/log/journal",
		"/etc/os-release",
		"/etc/resolv.conf",
		"/etc/hostname",
		"/etc/machine-id",
		"/etc/locale.conf",
	)

	// ----- loopback-only outbound -----
	// Empty base allowlist means deny-all; replace with loopback so
	// self-monitoring probes (`curl 127.0.0.1:9101/health`) succeed.
	// Operators can extend via LoadFromYAML.
	p.NetworkHostAllowlist = []string{
		"127.0.0.0/8",
		"::1/128",
	}

	// ----- extra read-only binaries -----
	// kill: read-only mode = -l/-L (list signals). Any other argv =
	// rejected by class default (the write semantics — sending a
	// signal — are still flagged by ClassMixed's "argv does not match
	// a known read-only form" path). Approved via token if attempted.
	p.addBin(&BinaryPolicy{
		Bin:   "kill",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"-l", "-L"}},
		},
		// No WriteMatchers: relying on class-default reject so read
		// matchers can win first (WriteMatchers' catch-all would always
		// fire first, blocking the read path).
	})
	// pgrep: list processes by pattern. Always read.
	p.addBin(&BinaryPolicy{Bin: "pgrep", Class: ClassReadSystem})
	// pidof: print PIDs of named programs. Read.
	p.addBin(&BinaryPolicy{Bin: "pidof", Class: ClassReadSystem})
	// pkill: list / count modes = read; default = write (kill
	// processes). Read discr is explicit flag set; anything else is
	// rejected by class default. Arbitrary signal selection (-s /
	// --signal) is denied at the DeniedArgs layer.
	p.addBin(&BinaryPolicy{
		Bin:   "pkill",
		Class: ClassMixed,
		ReadOnlyMatchers: []ArgMatcher{
			{AnyFlag: []string{"--list", "-l", "--count", "-c", "--oldest", "-o", "--newest", "-n"}},
		},
		DeniedArgs: []string{"--signal", "-s"},
	})

	// Resolve abs paths for the new binaries. (DefaultReadOnly
	// already resolved everything it registered; we only need to
	// resolve what we added.)
	for _, name := range []string{"kill", "pgrep", "pidof", "pkill"} {
		if bp, ok := p.bins[name]; ok && bp.AbsPath == "" {
			bp.AbsPath = discoverBin(name)
		}
	}
	return p
}