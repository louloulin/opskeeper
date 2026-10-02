package pluginimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/floor/pluginmanifest"
	"github.com/vincent-wuhan/opskeeper/core/manager/biz/aiops/chatruntime"
)

// write puts a file at rel under dir, creating parents.
func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// skill is a minimal SKILL.md the loader accepts.
const skill = `---
name: demo-skill
description: A skill used by the importer tests.
when_to_use: when the tests need a skill
---

# Demo

Body.
`

// claudeContainer builds the `.claude-plugin/plugin.json` form.
func claudeContainer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, ".claude-plugin/plugin.json",
		`{"id":"acme-tools","name":"Acme Tools","version":"1.4.0","description":"Acme operational tools."}`)
	write(t, dir, "skills/acme-diagnose/SKILL.md", skill)
	write(t, dir, "agents/acme-triage.md", "---\nname: acme-triage\ndescription: triage\n---\n\nTriage.\n")
	write(t, dir, "commands/acme-note.md", "note body\n")
	return dir
}

// openclawContainer builds the `openclaw.plugin.json` form.
func openclawContainer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "openclaw.plugin.json",
		`{"id":"acme-legacy","name":"Acme Legacy","version":"0.9.1","description":"An openclaw-era pack."}`)
	write(t, dir, "skills/acme-diagnose/SKILL.md", skill)
	return dir
}

// bareSkillsContainer builds the skills.sh form: no manifest at all.
func bareSkillsContainer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "skills/dropped-skill/SKILL.md", skill)
	return dir
}

func TestEachContainerFormConvertsToALoadablePackage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		make   func(*testing.T) string
		want   string
		derive bool // no manifest carries the name; it comes from the directory
	}{
		{"claude", claudeContainer, "acme-tools", false},
		{"openclaw", openclawContainer, "acme-legacy", false},
		{"bare skills", bareSkillsContainer, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.make(t)
			dest := filepath.Join(t.TempDir(), "out")

			report, err := Import(Options{Source: src, Dest: dest})
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if report.Kind == chatruntime.ContainerNone {
				t.Errorf("kind = none, want a recognised container")
			}
			if report.Name == "" {
				t.Error("a converted package must always be named")
			} else if !tc.derive && report.Name != tc.want {
				t.Errorf("name = %q, want %q", report.Name, tc.want)
			}

			// The generated package is proven loadable by the same loader
			// the control plane will admit it with, rather than by a
			// second parser here that could disagree.
			p, err := pluginmanifest.Load(dest)
			if err != nil {
				t.Fatalf("the generated package does not load: %v", err)
			}
			if !p.RunsOn(domain.TargetEdge) {
				t.Error("a converted package should target the edge by default")
			}
			if len(p.Skills) == 0 {
				t.Error("the skills did not survive the conversion")
			}
		})
	}
}

func TestAConvertedPackageIsInertUntilSomebodyReviewsIt(t *testing.T) {
	// The whole point of the importer's policy. A converter that filled in
	// the tool list would be claiming to have reviewed code it only read
	// the names of, and the host would build an allow-list from that claim.
	src := claudeContainer(t)
	dest := filepath.Join(t.TempDir(), "out")

	report, err := Import(Options{Source: src, Dest: dest})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	p, err := pluginmanifest.Load(dest)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Manifest.Spec.Tools) != 0 {
		t.Errorf("the converted package declares %d tools; an unreviewed import must declare none",
			len(p.Manifest.Spec.Tools))
	}
	if p.HighestCapability() != domain.ClassRead {
		t.Errorf("highest capability = %q, want read", p.HighestCapability())
	}
	if len(p.Manifest.Spec.RequiredScopes) != 0 {
		t.Errorf("the converted package asks for %v; an unreviewed import must ask for nothing",
			p.Manifest.Spec.RequiredScopes)
	}
	if p.Manifest.Spec.Install.Strategy != domain.InstallPin {
		t.Errorf("install strategy = %q, want pin: a package nobody has read must not auto-upgrade onto a node fleet",
			p.Manifest.Spec.Install.Strategy)
	}
	if len(report.Decisions) == 0 {
		t.Error("an import must say what is still undecided")
	}
}

func TestTheToolListIsAlwaysSomethingToDo(t *testing.T) {
	// Every converted package has the same three undecided fields, so the
	// report always has content. A converter that produced an empty report
	// would read as "nothing left to review", which is the one thing an
	// import is never true of.
	src := bareSkillsContainer(t)
	report, err := Import(Options{
		Source: src,
		Dest:   filepath.Join(t.TempDir(), "out"),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	fields := map[string]bool{}
	for _, d := range report.Decisions {
		fields[d.Field] = true
		if d.Why == "" {
			t.Errorf("decision %q has no reason; a missing value and a value that cannot be inferred are different problems", d.Field)
		}
	}
	for _, want := range []string{"spec.tools", "spec.required_scopes", "spec.safety_level"} {
		if !fields[want] {
			t.Errorf("no decision reported for %s", want)
		}
	}
}

func TestAnImportReplacesNothing(t *testing.T) {
	// A merge would leave files from a previous version in place, and the
	// review surface for a package is its files.
	src := claudeContainer(t)
	dest := filepath.Join(t.TempDir(), "out")
	if _, err := Import(Options{Source: src, Dest: dest}); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if _, err := Import(Options{Source: src, Dest: dest}); err == nil {
		t.Error("a second import overwrote an existing package")
	}
}

func TestAnImportRefusesToWriteInsideItsOwnSource(t *testing.T) {
	// A converter that copied a directory into itself would recurse; one
	// that overwrote it would destroy the original before anybody looked.
	src := claudeContainer(t)
	if _, err := Import(Options{
		Source: src,
		Dest:   filepath.Join(src, "converted"),
	}); err == nil {
		t.Error("an import wrote inside its own source")
	}
}

func TestASourceThatIsNotAContainerIsRefusedWithWhatWasLookedFor(t *testing.T) {
	// The error names the three recognised forms, so somebody converting a
	// fourth one learns what to add rather than only that it failed.
	src := t.TempDir()
	_, err := Import(Options{Source: src, Dest: filepath.Join(t.TempDir(), "out")})
	if err == nil {
		t.Fatal("an empty directory was converted")
	}
	for _, want := range []string{".claude-plugin/plugin.json", "openclaw.plugin.json", "skills/<name>/SKILL.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestCommandsBecomePrompts(t *testing.T) {
	// PiG names that resource class `prompts`. A converter that left the
	// old name in place would produce a package whose commands are
	// silently undiscoverable, which looks exactly like a plugin that
	// shipped nothing.
	src := claudeContainer(t)
	dest := filepath.Join(t.TempDir(), "out")
	report, err := Import(Options{Source: src, Dest: dest})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Prompts == 0 {
		t.Error("the command files were dropped rather than remapped")
	}
	if _, err := os.Stat(filepath.Join(dest, "prompts")); err != nil {
		t.Errorf("no prompts directory was written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "commands")); err == nil {
		t.Error("a commands directory was written; PiG would not discover it")
	}
}

func TestExtensionsSurviveAndAreCalledOutAsADecision(t *testing.T) {
	src := claudeContainer(t)
	write(t, src, "extensions/acme/extension.js", "export default function () {}\n")
	dest := filepath.Join(t.TempDir(), "out")

	report, err := Import(Options{Source: src, Dest: dest})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Extension != 1 {
		t.Errorf("extensions = %d, want 1", report.Extension)
	}
	found := false
	for _, d := range report.Decisions {
		if strings.HasPrefix(d.Field, "extensions/") {
			found = true
		}
	}
	if !found {
		t.Error("an import that carries extensions must ask whether they are safe to run in-process")
	}
}

func TestAnAwkwardNameSurvivesTheRoundTrip(t *testing.T) {
	// A plugin called `123` or `yes` is a real thing, and an unquoted YAML
	// scalar would come back as a number or a boolean. The name is what
	// the whole catalogue is keyed on.
	for _, name := range []string{"123", "yes", "true", "null", "1.5", "has space", `quo"te`} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, ".claude-plugin/plugin.json",
				`{"id":`+quote(name)+`,"version":"1.0.0","description":"d"}`)
			write(t, dir, "skills/s/SKILL.md", skill)
			dest := filepath.Join(t.TempDir(), "out")

			report, err := Import(Options{Source: dir, Dest: dest})
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			p, err := pluginmanifest.Load(dest)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if p.Name() != name {
				t.Errorf("name = %q after the round trip, want %q (report said %q)", p.Name(), name, report.Name)
			}
		})
	}
}

func TestTheVersionIsAlwaysPresent(t *testing.T) {
	// A blank version is a load error, so a source that did not declare
	// one would convert into something nothing can install.
	src := t.TempDir()
	write(t, src, ".claude-plugin/plugin.json", `{"id":"noversion","name":"No Version"}`)
	write(t, src, "skills/s/SKILL.md", skill)
	dest := filepath.Join(t.TempDir(), "out")

	report, err := Import(Options{Source: src, Dest: dest})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Version != "0.0.0" {
		t.Errorf("version = %q, want the 0.0.0 default", report.Version)
	}
	if _, err := pluginmanifest.Load(dest); err != nil {
		t.Errorf("the generated package does not load: %v", err)
	}
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	// A container that links outside its own tree would otherwise be
	// copied as a file whose content came from somewhere the operator
	// never reviewed, and the review surface for a package is its files.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("credentials"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	src := claudeContainer(t)
	if err := os.Symlink(outside, filepath.Join(src, "skills", "leaked", "SKILL.md")); err != nil {
		if os.IsNotExist(err) {
			// The skills dir is already there; a missing parent means the
			// symlink test cannot run on this platform.
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatalf("symlink: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	if _, err := Import(Options{Source: src, Dest: dest}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "skills", "leaked", "SKILL.md")); !os.IsNotExist(err) {
		t.Error("a symlink was followed into the package")
	}
}

// quote renders a JSON string, for building fixture manifests.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
