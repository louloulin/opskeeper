package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree materialises a miniature repository: a doc under docs/api and the
// Go files that do or do not register what the doc claims.
//
// The fixtures are tiny on purpose. This command's whole job is comparing a
// claim against a route literal, and a fixture large enough to be realistic
// would mostly be testing the filesystem.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func claimsOf(t *testing.T, report Report) []string {
	t.Helper()
	out := make([]string, 0, len(report.Missing))
	for _, m := range report.Missing {
		out = append(out, m.Claim)
	}
	return out
}

func TestADocumentedEndpointServedByAGroupMountedRoute(t *testing.T) {
	// The shape this repository actually uses: the handler registers
	// "/v1/thing" and the router mounts the group at "/api".
	root := writeTree(t, map[string]string{
		"docs/api/thing.md": "```\nGET /api/v1/thing\n```\n",
		"cmd/app/main.go":   "func mount(r chi.Router) {\n\tr.Handle(\"/v1/thing\", h)\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 0 {
		t.Fatalf("missing = %v, want none", claimsOf(t, report))
	}
	if report.Endpoints != 1 {
		t.Errorf("endpoints claimed = %d, want 1", report.Endpoints)
	}
}

func TestAParameterInTheDocumentedPathMatchesAWildcard(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/thing.md": "```\nGET /api/v1/thing/{id}\n```\n",
		"cmd/app/main.go":   "func mount(r chi.Router) {\n\tr.Get(\"/thing/{id}\", h)\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 0 {
		t.Fatalf("missing = %v, want none: {id} is a documented parameter", claimsOf(t, report))
	}
}

// The case this whole command was written for: a document that reads like a
// delivered contract and describes a server that was never built.
func TestAnEndpointNobodyRegisteredIsReported(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/ghost.md": "```\nPOST /api/v1/harness/runs\n```\n",
		"cmd/app/main.go":   "func mount(r chi.Router) {\n\tr.Handle(\"/v1/other\", h)\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 1 || report.Missing[0].Claim != "endpoint /api/v1/harness/runs" {
		t.Fatalf("missing = %v, want the one ghost endpoint", claimsOf(t, report))
	}
	if report.Missing[0].Line != 2 {
		t.Errorf("line = %d, want 2: a finding a reader cannot jump to is a finding nobody reads", report.Missing[0].Line)
	}
}

// A glob pattern is not a route. The plugin loader carries "/*/pig-ops.yaml",
// and treating that "*" as a subtree mount made this gate call every
// documented endpoint in the repository served.
func TestAGlobPatternIsNotASubtreeMount(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/ghost.md": "```\nPOST /api/v1/harness/runs\n```\n",
		"cmd/app/glob.go":   "var pattern = \"/*/pig-ops.yaml\"\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 1 {
		t.Fatalf("missing = %v, want the glob not to count as a route", claimsOf(t, report))
	}
}

// A subtree mount is a route. chi's "/v1/thing/*" serves everything under it,
// and refusing that would make the gate report a delivered endpoint as a
// phantom — the failure mode that teaches people to add exemptions.
func TestASubtreeMountServesWhatIsUnderIt(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/thing.md": "```\nGET /api/v1/thing/{id}\n```\n",
		"cmd/app/main.go":   "func mount(r chi.Router) {\n\tr.Handle(\"/v1/thing/*\", h)\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 0 {
		t.Fatalf("missing = %v, want none", claimsOf(t, report))
	}
}

// Commenting a route out must unregister it. This is the hole a phantom is
// easiest conjured through: write the path in a comment and every check that
// reads strings goes green.
func TestARouteMentionedOnlyInACommentIsNotARoute(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/ghost.md": "```\nPOST /api/v1/harness/runs\n```\n",
		"cmd/app/note.go":   "// the router used to mount \"/api/v1/harness/runs\" here\n/* and \"/api/v1/harness/runs/{id}\" too */\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 1 {
		t.Fatalf("missing = %v, want the commented path not to count", claimsOf(t, report))
	}
}

// A test asserting a request is not a registration. Otherwise a single
// httptest.NewRequest line keeps a deleted endpoint documented for ever.
func TestARouteOnlyAssertedInATestIsNotARoute(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/ghost.md":    "```\nPOST /api/v1/harness/runs\n```\n",
		"cmd/app/main_test.go": "func TestGone(t *testing.T) {\n\thttp.NewRequest(\"POST\", \"/api/v1/harness/runs\", nil)\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 1 {
		t.Fatalf("missing = %v, want the test-only path not to count", claimsOf(t, report))
	}
}

func TestASubcommandClaimMustMatchTheDispatch(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/cli.md":            "```\nopskeeper-eval judge --input a.json\n```\n",
		"cmd/opskeeper-eval/main.go": "func main() {\n\tswitch sub {\n\tcase \"judge\":\n\t}\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 0 || report.Subcommands != 1 {
		t.Fatalf("missing = %v (claimed %d), want none claimed once", claimsOf(t, report), report.Subcommands)
	}
}

func TestAnUnimplementedSubcommandIsReported(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/cli.md":            "```\nopskeeper-eval leaderboard-check\n```\n",
		"cmd/opskeeper-eval/main.go": "func main() {\n\tswitch sub {\n\tcase \"judge\":\n\t}\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 1 || !strings.Contains(report.Missing[0].Claim, "leaderboard-check") {
		t.Fatalf("missing = %v, want the unimplemented subcommand", claimsOf(t, report))
	}
}

// The 未交付 sections of these docs name paths on purpose. Reading those as
// claims would force a doc to lie in order to be true about what is missing.
func TestProseAboutAMissingEndpointIsNotAClaim(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/thing.md": "这一节曾经描述 `GET /api/v1/harness/runs`，但它从未实现。\n\n" +
			"```\nGET /api/v1/thing\n```\n",
		"cmd/app/main.go": "func mount(r chi.Router) {\n\tr.Handle(\"/v1/thing\", h)\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 0 {
		t.Fatalf("missing = %v, want only the fenced block to count", claimsOf(t, report))
	}
	if report.Endpoints != 1 {
		t.Errorf("endpoints claimed = %d, want 1", report.Endpoints)
	}
}

// A documented {param} is a statement about the shape of the path. Letting it
// match any literal means "/x/{id}" is "served" by a route registered as
// "/x/cases", which is how "/api/v1/middleware" came to be reported as served
// by some unrelated "/{incident_id}".
func TestADocumentedParameterIsNotSatisfiedByALiteralSegment(t *testing.T) {
	root := writeTree(t, map[string]string{
		"docs/api/thing.md": "```\nGET /api/v1/thing/{id}\n```\n",
		"cmd/app/main.go":   "func mount(r chi.Router) {\n\tr.Get(\"/v1/thing/cases\", h)\n}\n",
	})
	report, err := check(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Missing) != 1 {
		t.Fatalf("missing = %v, want the literal segment not to satisfy {id}", claimsOf(t, report))
	}
}
