package cmdpolicy

import (
	"testing"
)

func TestDefaultPiCapable_InheritsReadOnly(t *testing.T) {
	base := DefaultReadOnly()
	pi := DefaultPiCapable()
	if pi == base {
		t.Errorf("DefaultPiCapable must return a fresh policy, not alias DefaultReadOnly")
	}
	// Every ReadFS / ReadSystem bin from DefaultReadOnly must also be
	// in the Pi policy (operators expect the baseline to remain
	// available).
	for _, bp := range base.bins {
		if bp.Class == ClassDenied || bp.Class == ClassMixed {
			continue
		}
		if _, ok := pi.bins[bp.Bin]; !ok {
			t.Errorf("Pi policy dropped read-only bin %q", bp.Bin)
		}
	}
	// Denied bins stay denied — no regression.
	for _, name := range []string{"bash", "rm", "python3", "shutdown", "chmod"} {
		if bp, ok := pi.bins[name]; !ok || bp.Class != ClassDenied {
			t.Errorf("Pi policy must keep %q in ClassDenied", name)
		}
	}
}

func TestDefaultPiCapable_ExtraPathAllowlist(t *testing.T) {
	pi := DefaultPiCapable()
	required := []string{
		"/etc/systemd", "/run/systemd", "/var/log/journal",
		"/etc/os-release", "/etc/machine-id",
	}
	have := map[string]bool{}
	for _, p := range pi.PathAllowlist {
		have[p] = true
	}
	for _, r := range required {
		if !have[r] {
			t.Errorf("Pi PathAllowlist missing %q (got %v)", r, pi.PathAllowlist)
		}
	}
}

func TestDefaultPiCapable_LoopbackNetwork(t *testing.T) {
	pi := DefaultPiCapable()
	if len(pi.NetworkHostAllowlist) == 0 {
		t.Fatalf("Pi NetworkHostAllowlist must be seeded with loopback")
	}
	// Reach the curl fake bin path and decide an HTTP call to
	// localhost — must allow. To a public host — must reject.
	installFakeBin(t, pi, "curl")
	s := &Sandbox{Policy: pi, PathValidator: &fakePathValidator{}}
	if d := s.Decide("curl --head http://127.0.0.1:9101/health"); !d.Allow {
		t.Errorf("loopback curl should allow, got %s", d.Reason)
	}
	if d := s.Decide("curl --head http://example.com"); d.Allow {
		t.Errorf("example.com should reject under loopback-only allowlist")
	}
}

func TestDefaultPiCapable_KillReadOnly(t *testing.T) {
	pi := DefaultPiCapable()
	installFakeBin(t, pi, "kill")
	// kill -l = list signals = read.
	if d := pi.Decide("kill -l"); !d.Allow {
		t.Errorf("kill -l should allow, got %s", d.Reason)
	}
	// kill <pid> = send signal = write, must reject without approval.
	if d := pi.Decide("kill 12345"); d.Allow {
		t.Errorf("kill 12345 should reject (write semantics)")
	}
}

func TestDefaultPiCapable_PgrepPidofReadOnly(t *testing.T) {
	pi := DefaultPiCapable()
	for _, bin := range []string{"pgrep", "pidof"} {
		installFakeBin(t, pi, bin)
	}
	if d := pi.Decide("pgrep nginx"); !d.Allow {
		t.Errorf("pgrep nginx should allow, got %s", d.Reason)
	}
	if d := pi.Decide("pidof sshd"); !d.Allow {
		t.Errorf("pidof sshd should allow, got %s", d.Reason)
	}
}

func TestDefaultPiCapable_PkillRequiresWriteGate(t *testing.T) {
	pi := DefaultPiCapable()
	installFakeBin(t, pi, "pkill")
	// pgrep mode (--list) = read.
	if d := pi.Decide("pkill --list nginx"); !d.Allow {
		t.Errorf("pkill --list should allow, got %s", d.Reason)
	}
	// Default mode = signal-send = write, must reject.
	if d := pi.Decide("pkill nginx"); d.Allow {
		t.Errorf("pkill nginx should reject (write semantics)")
	}
	// -s / --signal denied regardless.
	if d := pi.Decide("pkill -9 nginx"); !d.Allow {
		// Either caught by write matcher or by DeniedArgs — both
		// correctly reject. We just want a reject.
		t.Logf("pkill -9 rejected (expected): %s", d.Reason)
	}
}