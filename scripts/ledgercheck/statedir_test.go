package ledgercheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryStateDirTheCodeWritesHasAHostMount is the seventeenth check, and it
// answers a question the other sixteen do not: the deployment manifests.
//
// Everything else in this package reconciles the ledger against the tree.
// This one reconciles the INSTALL MANIFEST against the code, and it exists
// because a directory can be writable and still be wrong.
//
// The manager image chowns /var/lib/opskeeper to the same uid it runs as, so
// a path under it is writable in the container — nothing fails, nothing logs,
// the ledger's file is written. It is written into the container's writable
// layer, and `compose up` / an image upgrade replaces that layer. The ledger
// the file describes — a federation membership, a crystallisation streak —
// is a record whose entire reason for existing is surviving a restart, and
// the container's writable layer does not survive one. For those two paths a
// missing mount is not one fewer layer of insurance; it is the feature being
// off, while every surface that reports on it looks healthy.
//
// The image already says so for one of them:
//   Dockerfile.opskeeper:248  "Bind-mount this in production to keep them
//   across container restarts."
// and the manifest did not. A comment in one file asserting an invariant
// that another file violates is the exact shape that survives review, so the
// invariant is checked here instead of trusted.
//
// This does not judge whether a path ought to be mounted. It judges one thing:
// every /var/lib/opskeeper/<dir> the code names, and every one the manifest
// mounts, are the same set — modulo directories that are legitimately local
// to a build, each listed below with the reason it is one.

// composeMountRE captures the container side of a bind mount under the
// manager's state root. It keys on the state root and reads to the end of the
// line, because a mount is written as `host:container[:mode]` and the mode is
// not part of the directory name.
var composeMountRE = regexp.MustCompile(`:/var/lib/opskeeper/([A-Za-z0-9_.-]+)`)

// codeStateDirRE captures a manager state directory out of a Go string
// literal. It stops at the first path separator, so /var/lib/opskeeper/skills
// reads as "skills" and a nested /var/lib/opskeeper/system/skills reads as
// "system" — which is the right granularity, because the manifest mounts
// parents, not leaves.
var codeStateDirRE = regexp.MustCompile(`"/var/lib/opskeeper/([A-Za-z0-9_.-]+)`)

// stateDirAllowlisted is where a state directory that the code names but the
// manifest does not mount is allowed to sit, and why.
//
// It is a list and not a boolean because the reason is the part worth
// re-reading in a year. An entry without one is not allowed in: the reason is
// what distinguishes "this deployment shape does not use it" from "somebody
// forgot", and a bare name reads identically to both.
// Keys are bare directory names with no leading slash, which is what all
// three sources produce once their capture groups are written the same way.
// That uniformity is the whole point and it was arrived at the hard way: an
// earlier revision had the manifest's names slashed and the scripts' names
// bare, and every check that compared the two reported all nine directories
// as unaccounted for — including five that had carried a chown line since
// before this file existed. A comparison whose two sides disagree about
// spelling fails in the direction that looks like a large finding, which is
// the one direction a reader is most likely to believe.
var stateDirAllowlisted = map[string]string{
	"db": "the install manifest runs OPSKEEPER_DB_DIALECT=mysql, so " +
		"openSQLite is never reached on this path; the same manifest tells " +
		"an operator switching to sqlite to add the mount themselves, " +
		"next to the line it already carries about the data volume",
}

func stateRoots(t *testing.T) []string {
	t.Helper()
	var roots []string
	for _, mod := range []string{"cmd", "core"} {
		err := filepath.WalkDir(filepath.FromSlash("../../"+mod), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range codeStateDirRE.FindAllStringSubmatch(string(raw), -1) {
				roots = append(roots, filepath.ToSlash(path)+" "+m[1])
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", mod, err)
		}
	}
	sort.Strings(roots)
	return roots
}

// Two limits of this reconciliation, stated here rather than discovered later.
//
// It reads the manifest as text and does not parse which service a volume
// belongs to, so a /var/lib/opskeeper mount under some OTHER service would be
// counted as the manager's and asked to be chowned to 65532. That is the
// conservative direction — a false demand, not a missed one — and no such
// mount exists today.
//
// It also reads the INSTALL manifest only. deploy/docker-compose.yml, the
// development shape, deliberately keeps nothing under /var/lib/opskeeper on
// the host: losing a container's state between `compose up`s is the normal
// expectation of a development stack, and making it durable would hide
// exactly the kind of state a developer wants thrown away. The helm chart is
// not read either, because it mounts the whole state root as one PVC and so
// has no per-directory list to disagree about.
func mountedStateDirs(t *testing.T) map[string]string {
	t.Helper()
	const manifest = "../../deploy/install/docker-compose.yml"
	raw, err := os.ReadFile(filepath.FromSlash(manifest))
	if err != nil {
		t.Fatalf("read %s: %v", manifest, err)
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(raw), "\n") {
		for _, m := range composeMountRE.FindAllStringSubmatch(line, -1) {
			out[m[1]] = fmt.Sprintf("%s:%d", manifest, i+1)
		}
	}
	return out
}

func TestEveryStateDirTheCodeWritesHasAHostMount(t *testing.T) {
	written, mounted := map[string]string{}, mountedStateDirs(t)
	for _, hit := range stateRoots(t) {
		i := strings.LastIndex(hit, " ")
		written[hit[i+1:]] = hit[:i]
	}
	if len(written) == 0 {
		t.Fatal("no manager state directory was found in the tree, so this check is reading nothing")
	}
	if len(mounted) == 0 {
		t.Fatal("no manager state directory is mounted in the install manifest, so this check is reading nothing")
	}

	// The manager writes somewhere the install manifest does not keep. For
	// every state directory that exists to outlive a process, that is the
	// feature being off with nothing reporting it.
	var missing []string
	for dir := range written {
		if _, ok := mounted[dir]; ok {
			continue
		}
		if reason, ok := stateDirAllowlisted[dir]; ok {
			if reason == "" {
				t.Errorf("state dir %q is allowlisted with no reason, which is indistinguishable from a forgotten mount", dir)
			}
			continue
		}
		missing = append(missing, fmt.Sprintf("%s (named in %s)", dir, written[dir]))
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the manager writes into %d state director(ies) the install manifest does not keep:\n  %s\n"+
			"either add the mount, or record why this shape does not need it in stateDirAllowlisted — "+
			"a writable container layer is not a restart, and the records these files hold exist to outlive one",
			len(missing), strings.Join(missing, "\n  "))
	}

	// And the other direction: a mount for a directory no code names is a
	// directory an operator will be told exists and never populated.
	var stale []string
	for dir, where := range mounted {
		if _, ok := written[dir]; !ok {
			stale = append(stale, fmt.Sprintf("%s (mounted at %s)", dir, where))
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("the install manifest keeps %d state director(ies) no code names:\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}

// TestEveryMountedStateDirIsChownedByBothInstallScripts is the second half of
// the same reconciliation, and it is the half that turns a mount into a
// working one.
//
// A bind mount the host directory does not own is worse than no mount at
// all. Docker creates a missing host directory as root on first `up`; the
// manager image runs as uid 65532; so the directory exists, the mount is in
// the manifest, `docker compose ps` looks healthy — and the manager cannot
// write to it. The three failure modes are all silent degradation rather than
// a crash, which is why nothing above the file notices:
//
//   federation/   → the root forgets its cluster set and re-enrols everyone
//   crystallize/ → promotion streaks restart from zero on every `compose up`
//   repos/       → every knowledge repo is re-cloned
//
// install.sh already says this out loud ("Without chown, docker creates them
// root-owned on first `up` and the nonroot manager can't write") and already
// enumerates the directories it knows about. The enumeration is the fragile
// part: a mount added to the manifest does not add itself to a shell loop
// written in another file, and the result is a deployment that is configured
// correctly and persists nothing. This is the check that would have caught
// the four mounts this decision added — and it is written after adding them
// rather than before, which is the honest note on its own value.

// chownRE captures the state directory a `chown -R` re-owns to the manager's
// uid. Only the manager's own uid is captured; a mount for another service
// (mysql, grafana) is chowned to that service's uid and is not this check's
// business.
var chownRE = regexp.MustCompile(`chown -R +65532:65532 +"\$OPSKEEPER_DATA_DIR/([A-Za-z0-9_.-]+)"`)

func chownedStateDirs(t *testing.T, script string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(script))
	if err != nil {
		t.Fatalf("read %s: %v", script, err)
	}
	out := map[string]bool{}
	for _, m := range chownRE.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	return out
}

// mkdirStateDirRE matches a state directory named in a shell script. It is
// applied only to the lines a `mkdir` command spans, because the same
// variable appears in the chown lines too and matching those would make the
// mkdir check pass on the strength of a chown — which is the exact inversion
// this check exists to catch, since a chown of a directory that does not exist
// yet is a no-op.
var mkdirStateDirRE = regexp.MustCompile(`"\$OPSKEEPER_DATA_DIR/([A-Za-z0-9_.-]+)"`)

func mkdirStateDirs(t *testing.T, script string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(script))
	if err != nil {
		t.Fatalf("read %s: %v", script, err)
	}
	out := map[string]bool{}
	spanning := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !spanning && !strings.Contains(line, "mkdir") {
			continue
		}
		for _, m := range mkdirStateDirRE.FindAllStringSubmatch(line, -1) {
			out[m[1]] = true
		}
		// A backslash continues the command; anything else ends it.
		spanning = strings.HasSuffix(strings.TrimRight(line, " \t"), "\\")
		if !spanning {
			continue
		}
	}
	return out
}

func TestEveryMountedStateDirIsChownedByBothInstallScripts(t *testing.T) {
	mounted := mountedStateDirs(t)
	if len(mounted) == 0 {
		t.Fatal("no manager state directory is mounted in the install manifest, so this check is reading nothing")
	}

	for _, script := range []string{"../../deploy/install/install.sh", "../../deploy/install/upgrade.sh"} {
		chowned, created := chownedStateDirs(t, script), mkdirStateDirs(t, script)
		if len(chowned) == 0 || len(created) == 0 {
			t.Fatalf("%s re-owns %d and creates %d state directories, so this check is reading nothing",
				script, len(chowned), len(created))
		}
		for _, want := range []struct {
			label string
			have  map[string]bool
			why   string
		}{
			{"chown", chowned, "docker creates a missing host directory as root and the image runs as 65532, " +
				"so the mount then exists and is unwritable — which reads as a working feature and is not one"},
			{"mkdir", created, "a chown of a directory that does not exist yet is a no-op, and it runs before " +
				"`compose up` is what would create it"},
		} {
			var absent []string
			for dir := range mounted {
				if !want.have[dir] {
					absent = append(absent, fmt.Sprintf("%s (mounted at %s)", dir, mounted[dir]))
				}
			}
			sort.Strings(absent)
			if len(absent) > 0 {
				t.Errorf("%s does not %s %d mounted state director(ies):\n  %s\n%s",
					script, want.label, len(absent), strings.Join(absent, "\n  "), want.why)
			}
		}
	}
}
