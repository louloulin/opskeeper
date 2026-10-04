package ledgercheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The third number the repository can answer for itself: how many modules
// OpsKeeper is split into.
//
// The Makefile is the source of truth rather than a constant written here,
// because PIG_MODULES is the list `module-standalone-check` actually iterates
// and `module-standalone-check` is the gate that builds each one on its own
// published tags. A count written in a test would be a third copy of a list
// that already has two, and the copy nobody maintains is the one that gets
// quoted.

const makefilePath = "../../Makefile"

// moduleCountRE reads the count the progress table's architecture row claims.
var moduleCountRE = regexp.MustCompile(`(\d+) 个模块落地`)

// declaredModules counts the entries of PIG_MODULES in the Makefile.
//
// The list is nine lines long and joined with trailing backslashes, so it is
// read line by line rather than with a single regexp. A regex written for this
// compiles, passes its own reading of the file, and silently stops after the
// first line — which reports seven of fourteen modules and turns the check
// below into a permanent false positive that nobody would take seriously.
func declaredModules(t *testing.T) (int, []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(makefilePath))
	if err != nil {
		t.Fatalf("read the Makefile: %v", err)
	}

	var fields []string
	collecting := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if collecting {
			fields = append(fields, strings.Fields(strings.TrimSuffix(strings.TrimSpace(line), "\\"))...)
			if !strings.HasSuffix(line, "\\") {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "PIG_MODULES :=") {
			collecting = true
			fields = append(fields, strings.Fields(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, "PIG_MODULES :=")), "\\"))...)
			if !strings.HasSuffix(line, "\\") {
				break
			}
		}
	}
	if !collecting {
		t.Fatal("the Makefile declares no PIG_MODULES; module-standalone-check iterates that list, so a check against it would be checking nothing")
	}
	if len(fields) == 0 {
		t.Fatal("PIG_MODULES is empty")
	}
	return len(fields), fields
}

// planARow returns the architecture row of the progress table.
func planARow(t *testing.T, ledger string) string {
	t.Helper()
	row := progressRow(t, ledger, "| A 模块化地基 |")
	return row
}

// TestTheProgressTableCountsTheModulesTheMakefileDeclares is the check for the
// "13 个模块" that should have read 14.
//
// It is a gate rather than a note because the number only changes when someone
// adds or removes a module — which is exactly the kind of change that ought to
// make somebody look at the architecture row again.
func TestTheProgressTableCountsTheModulesTheMakefileDeclares(t *testing.T) {
	declared, list := declaredModules(t)
	row := planARow(t, readLedger(t))

	m := moduleCountRE.FindStringSubmatch(row)
	if m == nil {
		t.Fatalf("the architecture row quotes no module count (`<n> 个模块落地`), so nothing can be checked; "+
			"the Makefile declares %d: %s", declared, strings.Join(list, " "))
	}
	claimed, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("unreadable module count %q: %v", m[1], err)
	}
	if claimed != declared {
		t.Errorf("the progress table says %d modules; the Makefile's PIG_MODULES lists %d (%s)",
			claimed, declared, strings.Join(list, " "))
	}
}

// progressRow returns the first row of the progress table whose line starts
// with prefix.
//
// It scopes the search to the progress section on purpose. Several decisions
// carry their own copy of these tables, and those are history: decision 108's
// D row still says four packages, which was true when it was written.
// Rewriting them would be falsifying the record, so the checks read the
// progress section and only that one.
func progressRow(t *testing.T, ledger, prefix string) string {
	t.Helper()
	start := strings.Index(ledger, progressHeading)
	if start < 0 {
		t.Fatalf("the ledger has no %q section; this check reads the progress table and has to be told where it moved", progressHeading)
	}
	rest := ledger[start:]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	for _, line := range strings.Split(rest, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("no line starting with %q inside %q; the progress table's shape changed", prefix, progressHeading)
	return ""
}
