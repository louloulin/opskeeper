package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests check the file without a YAML library, because core/edge may
// not take one: the module's allowed set is core's contracts and the
// standard library, and a test is held to the same rule as production code
// so that a boundary does not quietly become negotiable in test files. The
// field reader below is therefore hand-written, and it is hand-written on
// purpose — the assertion it has to support is an *absent-versus-empty*
// distinction, and a typed struct would collapse exactly that.
//
//   tools: []      -> no PiG built-ins. This is the whole point of the file.
//   (no tools:)    -> PiG's normal built-ins, which on a node include bash.
//
// The proof that PiG itself accepts this file and reads it that way lives in
// core/pig/pigprofile, which is the one module allowed to know what PiG's
// API looks like this week.

// topLevel reads one column-0 key out of the profile.
//
// Comments and blank lines are skipped, and a key is only recognised at
// column 0 — which is what makes `tools:` inside the comment block
// indistinguishable from the real one, so the comments in Render must never
// use that spelling at column 0. It is a narrow reader on purpose: anything
// cleverer would be reimplementing a YAML parser badly.
func topLevel(t *testing.T, body, key string) (value string, found bool) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // nested, so not the key being asked about
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok || name != key {
			continue
		}
		return strings.TrimSpace(rest), true
	}
	return "", false
}

func TestTheProfileIsNamedForWhatItIs(t *testing.T) {
	name, found := topLevel(t, Render(), "name")
	if !found {
		t.Fatal("the profile has no name; it is what an operator sees in the agent's own diagnostics")
	}
	if name != Name {
		t.Errorf("name = %q, want %q", name, Name)
	}
	desc, found := topLevel(t, Render(), "description")
	if !found || len(strings.Trim(desc, `"`)) == 0 {
		t.Error("the profile has no description; a node agent that names nothing explains nothing in pig status")
	}
}

// TestTheProfileRemovesEveryBuiltinTool is the load-bearing assertion, and
// it is about presence rather than value.
func TestTheProfileRemovesEveryBuiltinTool(t *testing.T) {
	value, found := topLevel(t, Render(), "tools")
	if !found {
		// The failure this guards is the entire reason the file exists.
		// An omitted `tools` means PiG's stock built-ins, and one of them
		// is a shell on a host running with the node's privileges.
		t.Fatal(`the profile has no "tools" field; omitted means PiG's stock built-ins, which ` +
			"include a shell, and the field must be present and empty")
	}
	if value != "[]" {
		t.Errorf("tools = %q, want \"[]\"; anything named here is a tool the model is offered on a node", value)
	}
}

func TestTheProfileTurnsOffAmbientDiscovery(t *testing.T) {
	for _, kind := range []string{"extensions", "skills"} {
		line := "  " + kind + ":"
		if !strings.Contains(Render(), line+"\n    []") && !strings.Contains(Render(), line+" []") {
			t.Errorf("discovery.%s is not an empty list; a named ambient source is a resource the "+
				"model may invoke without ever appearing in a manifest", kind)
		}
	}
	// Both keys have to exist. discovery itself present but one kind
	// missing is a profile whose protection depends on which field a
	// future editor happened to keep.
	disc, found := topLevel(t, Render(), "discovery")
	if !found {
		t.Fatal("the profile has no discovery block; without it the agent finds skills in its home " +
			"directory that never passed a review")
	}
	_ = disc
}

// TestTheProfileGrantsNothingItself keeps this file honest about what it
// is. A profile that named extensions or skills would be a second,
// unreviewed way to put tools on a node; this one stays a pure subtraction,
// and the only reason that is safe is that the host's allow-list is what
// actually decides.
func TestTheProfileGrantsNothingItself(t *testing.T) {
	for _, field := range []string{"extensions", "skills", "packages", "secrets", "agentEnv", "model"} {
		if _, found := topLevel(t, Render(), field); found {
			t.Errorf("the profile declares %q at the root; a profile that adds resources is a second "+
				"way to put tools on a node that never passed a manifest review", field)
		}
	}
}

func TestWriteLeavesAReadableProfileAndNothingElse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent", ".pig")
	path, err := Write(dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("Write wrote to %s, want it inside %s", path, dir)
	}
	if filepath.Base(path) != FileName {
		t.Errorf("Write named the file %q, want %q", filepath.Base(path), FileName)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the written profile: %v", err)
	}
	if string(body) != Render() {
		t.Error("the written profile differs from Render(); a node's profile is a review surface " +
			"and a difference between what is reviewed and what runs defeats it")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the profile: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("mode = %o, want 640; the agent reads it as the user it runs as and nothing else on "+
			"the host has any business editing what runs here", perm)
	}

	// Nothing else is left behind. A staging file that survived a crash
	// would sit in the directory that decides what runs, and the next boot
	// would have to decide what to do with it.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the config directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the config directory holds %v, want only %s", names, FileName)
	}
}

func TestWriteIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".pig")
	first, err := Write(dir)
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}
	second, err := Write(dir)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if first != second {
		t.Errorf("Write returned %s then %s; the path has to be stable because it is passed to the "+
			"agent on every spawn", first, second)
	}
	body, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read the profile: %v", err)
	}
	if string(body) != Render() {
		t.Error("a second Write did not reproduce the same profile")
	}
}

func TestWriteRefusesAnEmptyDirectory(t *testing.T) {
	// A node with no agent directory has been misconfigured, and the
	// profile is not optional. Writing it somewhere else would start an
	// agent whose profile nobody can find.
	if path, err := Write("  "); err == nil {
		t.Errorf(`Write("  ") returned %s; an unset agent directory must stop the node`, path)
	}
}
