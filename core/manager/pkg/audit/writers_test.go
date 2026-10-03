package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	throatPath  = "github.com/vincent-wuhan/opskeeper/core/manager/biz/audit"
	rowTypePath = "github.com/vincent-wuhan/opskeeper/core/manager/model/audit"
	managerRoot = "../.."
)

// throatHolders are the packages allowed to hold the write path.
//
// The rule is not "the audit domain may write" — it is that exactly one
// throat writes, and everyone else asks to be remembered through the port
// above. A package on this list is on it because it is the writer, an
// adapter onto the writer, or a test that needs a real one behind it; the
// reason is not decoration, it is what the next person reads before
// adding themselves.
//
// Decision 35 put this throat in biz/audit so that every HLD-010 row
// passes through one place, and that remains true. What decision 109
// removed was the other half of the same decision leaking outward: naming
// a row required importing the writer, so six domains imported it to say
// "this happened". The list below is what is left after that, and it is
// short enough to read.
// biz/audit is absent on purpose: it is the definition of the throat, not a
// holder of it, and a package never imports itself. If a file inside it
// ever does, that is a cycle and the compiler will say so before this test
// gets a chance to.
var throatHolders = map[string]string{
	"server/middleware": "the audit middleware enriches the request (status, IP, request id) " +
		"and is the only thing that turns a handler's request into a call to the writer",
	"server/audit": "the ledger's own reader: it lists rows, reports chain state and " +
		"surfaces ErrChainDisabled, so it holds the usecase rather than a copy of it",
	"biz/chatdiagnose": "AuditAdapter wraps the usecase to satisfy chatdiagnose's own " +
		"logger port; the seam is the interface, the write is still the writer's",
	"biz/aiops/agentkernel": "LedgerWriter is the agent kernel's writer seam, and the row " +
		"it writes is an agent action rather than a user action",
	"service/frontierbound": "autonomy replay writes the decisions a node made on its own " +
		"back into the chain when the tunnel came back (decision 101), and a node's own " +
		"ledger — every tool call, block, agent turn and plugin install it recorded — " +
		"travels the same way (decision 126)",
	"server/plugin": "its test builds a real usecase to assert a plugin release lands in " +
		"the chain; the production handler uses the port",
}

// rowTypeReaders are the packages allowed to name the persisted row.
//
// model/audit holds two things now that the vocabulary moved out (decision
// 109): the GORM entities, and re-exported constants. The entities are
// storage, and storage has to be readable by exactly the code that already
// understood the table: the store that writes it, the ledger view that
// lists it, and the change-event tool that joins a configuration change to
// the operator who made it.
var rowTypeReaders = map[string]string{
	"data/audit/store":         "persistence: the entity, the chain head, the migration",
	"server/audit":             "the ledger view lists and filters rows",
	"biz/audit":                "the writer maps an Event onto the entity",
	"biz/aiops/tools/alerting": "query_change_events joins a change to the row that authorised it",
	"server/plugin":            "its test asserts on persisted rows",
	"server/middleware":        "its test asserts on the row the middleware emitted",
}

// TestOnlyTheThroatHoldsTheWriter is the manager-wide form of the rule
// decision 109 applied to iam.
//
// iam was fixed one domain at a time, and the other five were still
// importing the writer to name a row: alert, knowledge, setting, plugin and
// the MCP surface. Each of those edges looked harmless — a handler builds
// an Event and hands it to SetAuditEvent — and together they meant six
// domains could reach the one component whose entire value is that it is
// the only way a row gets written. A boundary that six packages lean on is
// a boundary with six people's changes behind it.
//
// The check walks the whole module rather than one context, because the
// defect was never context-local: each domain looked fine on its own.
func TestOnlyTheThroatHoldsTheWriter(t *testing.T) {
	files := walkManager(t)
	if len(files) == 0 {
		t.Fatal("no files were parsed; the walk is broken, not the boundary")
	}

	seenThroat := map[string]bool{}
	seenRow := map[string]bool{}
	for _, pf := range files {
		dir := rel(pf.path)
		for _, imp := range pf.file.Imports {
			target, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Errorf("%s: unquote import: %v", pf.path, err)
				continue
			}
			switch target {
			case throatPath:
				seenThroat[dir] = true
				if why, ok := throatHolders[dir]; !ok {
					t.Errorf("%s imports the audit throat (%s). Name the row through "+
						"core/manager/pkg/audit instead; the writer does not move, only the shape does",
						pf.path, target)
				} else if testing.Verbose() {
					t.Logf("%s may hold the writer: %s", dir, why)
				}
			case rowTypePath:
				seenRow[dir] = true
				if _, ok := rowTypeReaders[dir]; !ok {
					t.Errorf("%s imports the row entity (%s). If it only needs to name a "+
						"row, use core/manager/pkg/audit; if it genuinely persists or lists rows, "+
						"add the package to rowTypeReaders with the reason",
						pf.path, target)
				}
			}
		}
	}

	// The tables are only trustworthy if they are not quietly shrinking: a
	// reason that stopped being true should be deleted, and a package that
	// stopped reaching for the writer should be removed from the list.
	for dir := range throatHolders {
		if !seenThroat[dir] {
			t.Errorf("%s is listed as a throat holder but no longer imports it; delete the entry "+
				"and the reason with it", dir)
		}
	}
	for dir := range rowTypeReaders {
		if !seenRow[dir] {
			t.Errorf("%s is listed as a row reader but no longer imports the entity; delete the entry", dir)
		}
	}
}

// TestNoDomainOutsideTheListsReachesTheWriter is the same rule stated as a
// count, so the number is visible in a test log and in a review diff.
//
// Six domains reached for the writer to name a row before decision 110.
// The list above is the whole remaining set; if this count grows, a domain
// has started depending on the audit implementation again.
func TestNoDomainOutsideTheListsReachesTheWriter(t *testing.T) {
	files := walkManager(t)
	domains := map[string]bool{}
	for _, pf := range files {
		dir := rel(pf.path)
		if _, ok := throatHolders[dir]; ok {
			continue
		}
		for _, imp := range pf.file.Imports {
			target, _ := strconv.Unquote(imp.Path.Value)
			if target == throatPath {
				domains[dir] = true
			}
		}
	}
	if len(domains) > 0 {
		names := make([]string, 0, len(domains))
		for d := range domains {
			names = append(names, d)
		}
		t.Errorf("%d package(s) outside the throat holder list import the writer: %s",
			len(domains), strings.Join(names, ", "))
	}
}

// rel turns a walked path into a manager-relative package directory, which
// is how the two tables above are keyed. The walk yields "../.."-prefixed
// paths; the tables read like paths in the repository, and a table that
// says "../../../.." is a table nobody can check against a file path.
func rel(path string) string {
	dir := filepath.Dir(path)
	if trimmed := strings.TrimPrefix(filepath.ToSlash(dir), managerRoot+"/"); trimmed != dir {
		return trimmed
	}
	return filepath.ToSlash(dir)
}

type walkedFile struct {
	path string
	file *ast.File
}

// walkManager parses every Go file of the manager module. parser.ParseDir is
// not recursive and this tree is four levels deep at minimum.
func walkManager(t *testing.T) []walkedFile {
	t.Helper()
	fset := token.NewFileSet()
	var out []walkedFile
	err := filepath.WalkDir(managerRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		out = append(out, walkedFile{path: path, file: file})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the manager module: %v", err)
	}
	return out
}
