package pluginmanifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
)

// The review pipeline's tests are mostly about order.
//
// Each step is individually correct — the sdk validates a manifest, the
// signature verifies a tree, admission checks a policy — and the security
// of the whole thing rests on the sequence. A pipeline that admits first
// and verifies second would pass every one of these step-level tests and
// would be worthless, so the tests below are written to fail if the order
// changes.

// signingPolicy is a policy that admits the L1 packages the test helpers
// build, so a test about ordering is not also a test about admission.
func signingPolicy() Policy {
	return PolicyFor(domain.SafetyL3, domain.RadiusCluster,
		domain.Scopes{domain.ScopeHostRead, domain.ScopeHostWrite})
}

// signedPackage builds a valid, signed L1 package and returns it with a
// store that trusts its signer.
func signedPackage(t *testing.T, name string) (string, *TrustStore) {
	t.Helper()
	root := newPackage(t, name, "1.0.0")
	s := signerFor(t, "acme-2026")
	env, err := s.Sign(root)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := env.WriteTo(root); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return root, trustFor(t, s)
}

func TestASignedPackageThatFitsThePolicyIsAllowed(t *testing.T) {
	root, store := signedPackage(t, "acme-probe")

	d := Review(root, store, signingPolicy())
	if !d.Allowed {
		t.Fatalf("a signed, in-policy package was refused: %s", d)
	}
	if d.Plugin != "acme-probe" || d.Version != "1.0.0" {
		t.Errorf("decision names %s v%s, want acme-probe v1.0.0", d.Plugin, d.Version)
	}
	if d.Manifest.Metadata.Name != "acme-probe" {
		t.Errorf("the decision carries manifest %q; an allowed decision must carry what it allowed",
			d.Manifest.Metadata.Name)
	}
	if d.Envelope.KeyID != "acme-2026" {
		t.Errorf("the decision carries key %q, want the one that actually signed", d.Envelope.KeyID)
	}
}

func TestTheSignatureIsCheckedBeforeTheManifestIsBelieved(t *testing.T) {
	// An unsigned package whose manifest would be perfectly acceptable is
	// still refused, and refused at the *signature* step. If the manifest
	// were parsed first and trusted, this package would be admitted.
	//
	// The directory is deliberately named differently from the package
	// inside it, so the assertion can tell the two apart. A decision that
	// reported the manifest's name at this point would be reporting
	// something it has no reason to believe yet.
	root := newPackage(t, "acme-probe", "1.0.0")
	renamed := filepath.Join(filepath.Dir(root), "download-0042")
	if err := os.Rename(root, renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}
	root = renamed

	d := Review(root, NewTrustStore(), signingPolicy())
	if d.Allowed {
		t.Fatal("an unsigned package was admitted by a policy that would have accepted it")
	}
	if d.Step != StepSignature {
		t.Errorf("step = %q, want %q — the manifest must not be read before the tree is authenticated",
			d.Step, StepSignature)
	}
	if want := filepath.Base(root); d.Plugin != want {
		t.Errorf("the refused decision names %q, want the directory's name %q; the manifest "+
			"declares %q and nothing at this step has earned the right to repeat it",
			d.Plugin, want, "acme-probe")
	}
}

func TestAPackageExceedingTheNodePolicyIsRefusedAtAdmissionNotSignature(t *testing.T) {
	// The mirror image, and the reason the two steps are separate. This
	// package is genuine — right key, unmodified tree, valid manifest — and
	// it is still refused. Getting here is the point: the failure an
	// operator sees must name the policy ceiling, not accuse them of
	// running a forgery.
	root := t.TempDir()
	writeManifest(t, root, "acme-repair", "1.0.0", "L3", "write")
	s := signerFor(t, "acme-2026")
	env, err := s.Sign(root)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := env.WriteTo(root); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	d := Review(root, trustFor(t, s), PolicyFor(domain.SafetyL1, domain.RadiusNone,
		domain.Scopes{domain.ScopeHostRead}))
	if d.Allowed {
		t.Fatal("an L3 package was installed on a node whose ceiling is L1")
	}
	if d.Step != StepAdmission {
		t.Errorf("step = %q, want %q", d.Step, StepAdmission)
	}
	if d.Plugin != "acme-repair" {
		t.Errorf("the refusal names %q, want the package it refused; the signature was fine", d.Plugin)
	}
}

func TestTheSignatureIsCheckedBeforeAdmissionNotTheOtherWayRound(t *testing.T) {
	// The same package, unsigned. It would be refused either way, so the
	// *reason* is the assertion: an unsigned package must be caught by its
	// provenance, not by whatever the policy happened to say about it.
	// Otherwise a node that is misconfigured to be permissive would
	// silently start installing unsigned packages.
	root := t.TempDir()
	writeManifest(t, root, "acme-repair", "1.0.0", "L3", "write")

	d := Review(root, NewTrustStore(), PolicyFor(domain.SafetyL3, domain.RadiusCluster,
		domain.Scopes{domain.ScopeHostRead, domain.ScopeHostWrite}))
	if d.Allowed {
		t.Fatal("an unsigned L3 package was installed on a permissive node")
	}
	if d.Step != StepSignature {
		t.Errorf("step = %q, want %q — a permissive policy must not become a signing bypass", d.Step, StepSignature)
	}
}

func TestAnUnsignedPackageIsAllowedOnlyWhereThatWasAskedFor(t *testing.T) {
	// The development node. It has to exist, or nobody can iterate on a
	// plugin — but it has to be a decision, and the decision has to be
	// visible afterwards.
	root := newPackage(t, "acme-probe", "1.0.0")

	pol := signingPolicy()
	d := Review(root, NewTrustStore(), pol)
	if d.Allowed {
		t.Fatal("an unsigned package was admitted by the default policy")
	}

	pol.AllowUnsigned = true
	d = Review(root, NewTrustStore(), pol)
	if !d.Allowed {
		t.Fatalf("a development node refused an unsigned package it was told to accept: %s", d)
	}
	if d.Envelope.KeyID != "" {
		t.Errorf("the decision carries key %q; an unsigned install must not look like a verified one",
			d.Envelope.KeyID)
	}
}

func TestATamperedPackageIsRefusedEvenWhereUnsignedIsAllowed(t *testing.T) {
	// AllowUnsigned is a licence to skip the *check*, not to skip
	// reality. A package that carries a signature and then has its code
	// changed is still a package that failed verification, and a node
	// configured for development must not become a node that installs
	// whatever is on the disk.
	root, store := signedPackage(t, "acme-probe")
	write(t, filepath.Join(root, "extensions", "tool", "tools.go"), "package tool\n\n// replaced\n")

	pol := signingPolicy()
	pol.AllowUnsigned = true
	d := Review(root, store, pol)
	if d.Allowed {
		t.Fatal("a package with a broken signature was installed on a node that allows unsigned ones")
	}
	if d.Step != StepSignature {
		t.Errorf("step = %q, want %q", d.Step, StepSignature)
	}
}

func TestAVendorRestrictionNamesItself(t *testing.T) {
	// A package from a publisher this node does not work with is refused
	// with a reason the operator can act on. "Signature invalid" would
	// send them looking for a forgery that does not exist.
	root, store := signedPackage(t, "acme-probe")

	pol := signingPolicy()
	pol.AllowedVendors = []string{"opskeeper"}
	d := Review(root, store, pol)
	if d.Allowed {
		t.Fatal("a package from an unlisted vendor was admitted")
	}
	if d.Step != StepAdmission {
		t.Errorf("step = %q, want %q", d.Step, StepAdmission)
	}
	if !strings.Contains(d.Reason, "acme") {
		t.Errorf("reason = %q, want it to name the vendor that was refused", d.Reason)
	}

	pol.AllowedVendors = []string{"ACME"} // vendor matching is case-insensitive
	if d := Review(root, store, pol); !d.Allowed {
		t.Errorf("a listed vendor was refused on case alone: %s", d)
	}
}

func TestAManifestThatBreaksARuleIsRefusedEvenThoughItIsSigned(t *testing.T) {
	// A key is not a substitute for validation.
	//
	// This manifest is well-formed YAML with a real identity — so it can
	// be signed, and it is, with the node's own trusted key. What it is
	// not is valid: it declares a tool far above the capability ceiling it
	// claims. A pipeline that treated "signed" as "approved" would admit
	// it, and the node's allow-list would then contain a tool the
	// publisher never reviewed.
	root := filepath.Join(t.TempDir(), "acme-probe")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, filepath.Join(root, ManifestFile), `apiVersion: opskeeper.io/v1
kind: Plugin
metadata:
  name: acme-probe
  version: 1.0.0
  vendor: acme
spec:
  targets: [edge]
  safety_level: L1
  capabilities: [read]
  tools:
    - {name: host_probe_tcp, class: read}
    - {name: host_restart_service, class: destructive}
  required_scopes: [host.read]
  audit: {emits: true, mutates: false}
  approval: {required: false}
  install: {strategy: rolling}
`)
	s := signerFor(t, "acme-2026")
	env, err := s.Sign(root)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := env.WriteTo(root); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	d := Review(root, trustFor(t, s), signingPolicy())
	if d.Allowed {
		t.Fatal("a package whose manifest declares a tool above its own ceiling was admitted")
	}
	if d.Step != StepManifest {
		t.Errorf("step = %q, want %q — the signature is sound, so the manifest is what refused it", d.Step, StepManifest)
	}
	if d.Plugin != "acme-probe" {
		t.Errorf("decision names %q, want the manifest's own name; by now it has been read", d.Plugin)
	}
}

func TestAMissingScopeIsNamedRatherThanSummarised(t *testing.T) {
	// The refusal an operator has to act on. "some scopes are missing" is
	// not actionable; the missing names are.
	root, store := signedPackage(t, "acme-probe")

	d := Review(root, store, PolicyFor(domain.SafetyL3, domain.RadiusCluster,
		domain.Scopes{domain.ScopeTopologyRO}))
	if d.Allowed {
		t.Fatal("a package was admitted without the scopes it declares")
	}
	if d.Step != StepAdmission {
		t.Errorf("step = %q, want %q", d.Step, StepAdmission)
	}
	if !strings.Contains(d.Reason, string(domain.ScopeHostRead)) {
		t.Errorf("reason = %q, want it to name host.read as the scope that was not granted", d.Reason)
	}
}

func TestReviewAllReportsEveryPackageAndNotJustTheFirst(t *testing.T) {
	// A listing that stops at one bad package makes an operator believe
	// the rest are fine, which is the opposite of what they need.
	base := t.TempDir()
	good, store := signedPackage(t, "acme-good")
	copyTree(t, good, filepath.Join(base, "acme-good"))

	bad := newPackage(t, "acme-unsigned", "1.0.0")
	copyTree(t, bad, filepath.Join(base, "acme-unsigned"))

	decisions := ReviewAll(base, store, signingPolicy())
	if len(decisions) != 2 {
		t.Fatalf("got %d decisions, want one per package", len(decisions))
	}
	byName := map[string]Decision{}
	for _, d := range decisions {
		byName[d.Plugin] = d
	}
	if d := byName["acme-good"]; !d.Allowed {
		t.Errorf("the signed package was refused: %s", d)
	}
	if d := byName["acme-unsigned"]; d.Allowed {
		t.Error("the unsigned package was allowed")
	} else if d.Step != StepSignature {
		t.Errorf("the unsigned package was refused at %q, want %q", d.Step, StepSignature)
	}
}

func TestARefusedDecisionRendersSomethingAnOperatorCanRead(t *testing.T) {
	// Every refusal ends up in a log during an incident. The line has to
	// carry the package, the step and the reason, because the caller has
	// an id or two and no other context.
	root := newPackage(t, "acme-probe", "1.0.0")
	d := Review(root, NewTrustStore(), signingPolicy())

	// The package is named by its directory, because at the signature step
	// nothing about the package has been trusted yet — including whatever
	// its manifest calls itself. The operator still needs to know which
	// folder this was.
	line := d.String()
	for _, want := range []string{"acme-probe", StepSignature, "refused"} {
		if !strings.Contains(line, want) {
			t.Errorf("refusal line = %q, want it to mention %q", line, want)
		}
	}
}

// copyTree copies a package directory, which is how a test builds a
// catalog of several packages without a helper that knows about signing.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(t, s, d)
			continue
		}
		body, err := os.ReadFile(s)
		if err != nil {
			t.Fatalf("read %s: %v", s, err)
		}
		if err := os.WriteFile(d, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", d, err)
		}
	}
}
