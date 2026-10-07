package marketplace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The routes above are tested against a stub, which cannot catch the half of
// this that matters most: that the index is read from the directory
// packages actually land in. These tests use a real root with a real
// manifest in it.

func catalogRoot(t *testing.T, install string) string {
	t.Helper()
	root := t.TempDir()
	pkg := filepath.Join(root, "acme")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `apiVersion: opskeeper.io/v1
kind: Plugin
metadata:
  name: acme
  version: 1.0.0
  vendor: acme
  homepage: https://example.invalid/acme
spec:
  targets: [edge]
  safety_level: L1
  capabilities: [read]
  install:
    ` + install + `
  tools:
    - { name: acme.probe, class: read }
`
	if err := os.WriteFile(filepath.Join(pkg, "pig-ops.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return root
}

func catalogUsecase(root string) *Usecase {
	return NewUsecase(nil, nil, nil, Config{SystemSkillsRoot: root}, nil)
}

func catalogCaller() Caller { return Caller{UserID: 1, TenantID: 1, Role: "admin"} }

func TestCatalogReadsTheInstallRoot(t *testing.T) {
	uc := catalogUsecase(catalogRoot(t, "strategy: pin\n    min_edge_version: 0.8.0\n    min_pig_version: 0.4.0"))
	entries, err := uc.Catalog(context.Background(), catalogCaller())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries, want the one package in the root", len(entries))
	}
	if entries[0].Name != "acme" || entries[0].MinPigVersion != "0.4.0" {
		t.Errorf("entry = %#v", entries[0])
	}
}

// A tenant that has installed nothing is not an error. Answering 500 here
// would teach the SPA to render a failure where the truth is "empty".
func TestCatalogOnAnAbsentRootIsEmptyNotAnError(t *testing.T) {
	uc := catalogUsecase(filepath.Join(t.TempDir(), "never-created"))
	entries, err := uc.Catalog(context.Background(), catalogCaller())
	if err != nil {
		t.Fatalf("Catalog on an absent root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d entries from a root that does not exist", len(entries))
	}
}

// An unauthenticated call is refused before the filesystem is touched: the
// index says what a tenant may install, which is tenant-scoped information.
func TestCatalogRefusesAnAnonymousCaller(t *testing.T) {
	uc := catalogUsecase(catalogRoot(t, "strategy: rolling"))
	if _, err := uc.Catalog(context.Background(), Caller{}); err == nil {
		t.Error("an anonymous caller got the index")
	}
}
