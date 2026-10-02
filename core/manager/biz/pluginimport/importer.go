// Package pluginimport converts a legacy plugin container into a PiG
// package OpsKeeper can install.
//
// The legacy containers are three shapes the ecosystem already produces:
// the `.claude-plugin/plugin.json` form, the `openclaw.plugin.json` form,
// and the skills.sh drop of bare `skills/<name>/SKILL.md` directories. All
// three are read by the control plane today, as prompt and skill sources
// for the in-process agent. In 2.0 they become packages the node's own
// agent loads, and this is the bridge.
//
// The importer's governing rule is that it **invents nothing**. A legacy
// container carries no statement of what its tools do, no safety level, no
// scopes, and no blast radius — and every one of those is a decision
// somebody has to make deliberately before the code runs with a host's
// privileges. So the generated manifest declares the narrowest thing that
// loads: a read-only profile with no tools, and a report saying what is
// still undecided.
//
// The result is a package that installs, is inert, and is obviously
// incomplete. That is the correct output of a converter asked to translate
// a document that did not contain the information.
package pluginimport

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vincent-wuhan/opskeeper/core/domain"
	"github.com/vincent-wuhan/opskeeper/core/floor/pluginmanifest"
	"github.com/vincent-wuhan/opskeeper/core/manager/biz/aiops/chatruntime"
)

// resourceDirs are the package directories copied across, and the legacy
// directory each one comes from.
//
// `commands` is the only remap: PiG names that resource class `prompts`,
// and a converter that left the old name in place would produce a package
// whose commands are silently undiscoverable — which looks exactly like a
// plugin that shipped nothing.
var resourceDirs = []struct{ legacy, packaged string }{
	{"skills", "skills"},
	{"agents", "agents"},
	{"commands", "prompts"},
	{"prompts", "prompts"},
	{"mcp", "mcp"},
	{"extensions", "extensions"},
	{"hooks", "hooks"},
}

// Options configures an import.
type Options struct {
	// Source is the legacy container directory. Required.
	Source string
	// Dest is the package directory to write. Required. It must not
	// already exist: an import that merged into an existing package would
	// leave behind whatever the previous version had and nobody reviewed.
	Dest string
	// Vendor is recorded in the generated manifest. Optional.
	Vendor string
	// Targets is where the package may run. Defaults to the edge, which is
	// the only target a converted package is safe on: nothing here has
	// been reviewed for the control plane, and the control plane holds
	// identity, approval, and the audit ledger.
	Targets domain.Targets
}

// Decision is one thing a human still has to decide before the generated
// package can do anything.
//
// These are reported rather than guessed. A converter that filled them in
// would be claiming to have reviewed code it only read the names of.
type Decision struct {
	// Field is the manifest path, e.g. "spec.tools".
	Field string
	// Question is what has to be answered.
	Question string
	// Why is the answer not derivable — which is what makes this a
	// decision rather than a missing value.
	Why string
}

// Report is what an import produced.
type Report struct {
	// Kind is the container form that was recognised.
	Kind chatruntime.ContainerKind
	// Name, Version and Description are carried across from the source.
	Name        string
	Version     string
	Description string
	// Skills and Agents are the package-relative paths written.
	Skills []string
	Agents []string
	// Prompts, MCP and Extensions are the other resource counts, kept
	// rather than the paths: nobody reviews a count, and a report that
	// listed forty identical extension paths would be skimmed past.
	Prompts   int
	MCP       int
	Extension int
	// Decisions is what remains undecided. It is never empty for a
	// container that carried no governance, which is all of them.
	Decisions []Decision
	// Warnings are the loader's own non-fatal findings, carried through
	// so an import does not quietly drop a parse failure.
	Warnings []chatruntime.LoadWarning
}

// Import converts one container into a package.
//
// It writes to a staging directory and moves it into place only after the
// generated package has been validated by the same loader the control plane
// uses. An import that produced a package nothing could load would leave an
// operator with a directory and no explanation.
func Import(opts Options) (*Report, error) {
	if opts.Source == "" {
		return nil, errors.New("pluginimport: Source is required")
	}
	if opts.Dest == "" {
		return nil, errors.New("pluginimport: Dest is required")
	}
	source, err := filepath.Abs(opts.Source)
	if err != nil {
		return nil, fmt.Errorf("pluginimport: resolve source: %w", err)
	}
	dest, err := filepath.Abs(opts.Dest)
	if err != nil {
		return nil, fmt.Errorf("pluginimport: resolve dest: %w", err)
	}
	// Refuse to write inside the source: a converter that copied a
	// directory into itself would recurse, and one that overwrote it would
	// destroy the original before anybody looked at it.
	if within(dest, source) {
		return nil, fmt.Errorf("pluginimport: destination %s is inside the source %s", dest, source)
	}
	if _, err := os.Stat(dest); err == nil {
		return nil, fmt.Errorf("pluginimport: %s already exists; an import replaces nothing, because a merge would leave reviewed-by-nobody files behind", dest)
	}
	targets := opts.Targets
	if len(targets) == 0 {
		targets = domain.Targets{domain.TargetEdge}
	}

	// The loader does the recognising and the parsing. This package does
	// not have a second opinion about what counts as a container, because
	// two opinions would eventually disagree and the converter would be the
	// one that wins by being newer.
	result, err := chatruntime.LoadPluginContainer(source)
	if err != nil {
		return nil, fmt.Errorf("pluginimport: read %s: %w", source, err)
	}
	kind, _, err := chatruntime.DetectContainer(source)
	if err != nil {
		return nil, fmt.Errorf("pluginimport: detect %s: %w", source, err)
	}

	staging, err := os.MkdirTemp(filepath.Dir(dest), ".pluginimport-*")
	if err != nil {
		return nil, fmt.Errorf("pluginimport: stage: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	report := &Report{Kind: kind, Warnings: result.Warnings}
	report.Name = nameOf(result, source)
	report.Version = versionOf(result)
	report.Description = descriptionOf(result)

	if err := copyResources(source, staging, report); err != nil {
		return nil, err
	}
	if err := writeManifest(staging, report, opts, targets); err != nil {
		return nil, err
	}
	report.Decisions = decisionsFor(result, report)

	// The generated package is proven loadable before it is moved into
	// place, using the same loader that will admit it later. An import
	// that produced something the host could not read would be a
	// directory an operator had to debug by hand.
	if _, err := pluginmanifest.Load(staging); err != nil {
		return nil, fmt.Errorf("pluginimport: the generated package does not load: %w", err)
	}

	if err := os.Rename(staging, dest); err != nil {
		return nil, fmt.Errorf("pluginimport: install at %s: %w", dest, err)
	}
	return report, nil
}

// copyResources copies every recognised resource directory across.
func copyResources(source, staging string, report *Report) error {
	for _, dir := range resourceDirs {
		from := filepath.Join(source, dir.legacy)
		if _, err := os.Stat(from); err != nil {
			// A container is not required to have every resource class.
			// The two that are remapped share a destination, so the
			// second is skipped rather than allowed to clobber the first.
			continue
		}
		if dir.legacy == "prompts" && report.Prompts > 0 {
			continue
		}
		to := filepath.Join(staging, dir.packaged)
		n, err := copyTree(from, to)
		if err != nil {
			return fmt.Errorf("pluginimport: copy %s: %w", dir.legacy, err)
		}
		switch dir.packaged {
		case "skills":
			report.Skills = relFiles(staging, to)
		case "agents":
			report.Agents = relFiles(staging, to)
		case "prompts":
			report.Prompts = n
		case "mcp":
			report.MCP = n
		case "extensions":
			report.Extension = n
		}
	}
	return nil
}

// copyTree copies a directory, returning the number of files written.
//
// Symlinks are not followed. A container that links outside its own tree
// would otherwise be copied as a file whose content came from somewhere the
// operator never reviewed, and the review surface for a package is its
// files.
func copyTree(from, to string) (int, error) {
	n := 0
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(to, 0o750)
		}
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o750)
		}
		if d.Type()&os.ModeSymlink != 0 {
			// Recorded as zero copies rather than an error: a container
			// with a symlink is unusual, and refusing the whole import for
			// one would be a worse answer than importing the rest and
			// letting the operator see the count.
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := copyFile(path, dst); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

func copyFile(from, to string) (err error) {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(dst, src)
	return err
}

// relFiles lists a copied tree's files relative to the package root.
func relFiles(root, tree string) []string {
	var out []string
	_ = filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// within reports whether child is inside parent.
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// nameOf is the package name, falling back to the directory.
func nameOf(result *chatruntime.LoadResult, source string) string {
	if result.Pack != nil {
		if n := strings.TrimSpace(result.Pack.ID); n != "" {
			return n
		}
		if n := strings.TrimSpace(result.Pack.DisplayName); n != "" {
			return n
		}
	}
	return filepath.Base(source)
}

// versionOf is the source version, defaulted rather than left blank.
//
// The manifest refuses a blank version, and a converted package with no
// version would be un-updatable. Zero is the honest reading of "the source
// did not say", and it is comparable enough for a first import to be
// followed by a real one.
func versionOf(result *chatruntime.LoadResult) string {
	if result.Pack != nil {
		if v := strings.TrimSpace(result.Pack.Version); v != "" {
			return v
		}
	}
	return "0.0.0"
}

func descriptionOf(result *chatruntime.LoadResult) string {
	if result.Pack != nil {
		return strings.TrimSpace(result.Pack.Description)
	}
	return ""
}

// writeManifest emits the governance sidecar.
//
// The header is part of the output, not decoration. The file a reviewer
// opens is this one, and the most important thing it has to say is that
// everything below the header is a default nobody chose.
func writeManifest(dir string, report *Report, opts Options, targets domain.Targets) error {
	name := report.Name
	if name == "" {
		return errors.New("pluginimport: the source has no usable name")
	}
	version := report.Version
	if version == "" {
		version = "0.0.0"
	}

	var b strings.Builder
	b.WriteString("# pig-ops.yaml — GENERATED by pluginimport. Review before installing.\n")
	b.WriteString("#\n")
	b.WriteString("# Converted from a " + string(report.Kind) + " container. That format carries no\n")
	b.WriteString("# statement of what this package's code does, so nothing below was derived\n")
	b.WriteString("# from it. Every value here is the narrowest one that loads, chosen so the\n")
	b.WriteString("# package installs inert rather than not at all.\n")
	b.WriteString("#\n")
	b.WriteString("# The package currently declares no tools, which means the host's allow-list\n")
	b.WriteString("# is empty and every tool call it makes is refused. That is the intended\n")
	b.WriteString("# state of an unreviewed import. See spec.tools below.\n")
	b.WriteString("apiVersion: opskeeper.io/v1\n")
	b.WriteString("kind: Plugin\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + yamlScalar(name) + "\n")
	b.WriteString("  version: " + yamlScalar(version) + "\n")
	if opts.Vendor != "" {
		b.WriteString("  vendor: " + yamlScalar(opts.Vendor) + "\n")
	}
	b.WriteString("spec:\n")
	b.WriteString("  targets: [" + strings.Join(targetNames(targets), ", ") + "]\n")
	b.WriteString("\n")
	b.WriteString("  # L1: read-only, no approval. The one level a converter may choose,\n")
	b.WriteString("  # because it is the only one that adds no authority. Raising it is a\n")
	b.WriteString("  # review decision, and the level the tools below were reviewed at.\n")
	b.WriteString("  safety_level: L1\n")
	b.WriteString("\n")
	b.WriteString("  # Declared so the manifest loads. It is not a claim that the package is\n")
	b.WriteString("  # read-only — see spec.tools, which is what actually decides that.\n")
	b.WriteString("  capabilities: [read]\n")
	b.WriteString("\n")
	b.WriteString("  # THE ALLOW-LIST. Empty on purpose: no tool has been reviewed, so no tool\n")
	b.WriteString("  # is permitted, and the host refuses everything else. Add one line per tool\n")
	b.WriteString("  # this package is allowed to expose, with the class it actually has:\n")
	b.WriteString("  #\n")
	b.WriteString("  #   tools:\n")
	b.WriteString("  #     - { name: host_lsof, class: read }\n")
	b.WriteString("  #\n")
	b.WriteString("  # A tool listed here is a tool somebody agreed this package may run. A\n")
	b.WriteString("  # tool the agent finds that is not listed is refused on every turn.\n")
	b.WriteString("  tools: []\n")
	b.WriteString("\n")
	b.WriteString("  # Credentials are injected per scope and only per scope, so this list is\n")
	b.WriteString("  # the complete set of secrets this package will ever hold. Empty until\n")
	b.WriteString("  # somebody decides which ones it needs.\n")
	b.WriteString("  required_scopes: []\n")
	b.WriteString("\n")
	b.WriteString("  audit:\n")
	b.WriteString("    # The host derives every ledger entry from its own gate. A converted\n")
	b.WriteString("    # package has no way to write one, and cannot be given one.\n")
	b.WriteString("    emits: true\n")
	b.WriteString("    mutates: false\n")
	b.WriteString("\n")
	b.WriteString("  approval:\n")
	b.WriteString("    # No tool is permitted, so nothing reaches a queue. Both become true the\n")
	b.WriteString("    # moment a write-class or destructive-class tool is added above.\n")
	b.WriteString("    required: false\n")
	b.WriteString("\n")
	b.WriteString("  install:\n")
	b.WriteString("    # pinned, not rolling: a converted package is a starting point for review,\n")
	b.WriteString("    # and a package that auto-upgraded on a node fleet before it had been\n")
	b.WriteString("    # looked at would defeat the review entirely.\n")
	b.WriteString("    strategy: pin\n")

	path := filepath.Join(dir, pluginmanifest.ManifestFile)
	if err := os.WriteFile(path, []byte(b.String()), 0o640); err != nil {
		return fmt.Errorf("pluginimport: write manifest: %w", err)
	}
	return nil
}

func targetNames(targets domain.Targets) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, string(t))
	}
	return out
}

// yamlScalar quotes a value when leaving it bare would change its meaning.
//
// A plugin named `123` or `yes` is a real thing, and an unquoted one
// becomes a number or a boolean on the way back in. The importer's job is
// to produce a manifest that round-trips to the same name.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	safe := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '/':
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe && !isYamlBooleanOrNumber(s) {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// isYamlBooleanOrNumber reports whether a bare YAML scalar would come back
// as something other than a string.
func isYamlBooleanOrNumber(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	if s == "" {
		return true
	}
	// Anything that parses as a number comes back as one. The strconv
	// round trip is the test: a name that survives ParseFloat unchanged is
	// a number to YAML even if it looks like a name.
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err == nil {
		if got := fmt.Sprintf("%g", f); got == s {
			return true
		}
	}
	return false
}

// decisionsFor is the list of what a human still has to settle.
//
// Every entry says why the answer is not derivable, because "missing
// value" and "cannot be inferred without a review" are different problems
// and an operator should know which one they are looking at.
func decisionsFor(result *chatruntime.LoadResult, report *Report) []Decision {
	out := []Decision{
		{
			Field:    "spec.tools",
			Question: "Which tools may this package expose, and what class does each have?",
			Why: "The container format has no vocabulary for what a tool does. " +
				"Only reading the extension source answers it, and a class guessed " +
				"from a tool's name is exactly the guess the allow-list exists to prevent.",
		},
		{
			Field:    "spec.required_scopes",
			Question: "Which credentials does this package need?",
			Why: "Scopes are credentials, and a credential granted speculatively is a " +
				"credential in the hands of code nobody has read. " +
				"An empty list grants nothing and is the correct starting point.",
		},
		{
			Field:    "spec.safety_level",
			Question: "Is L1 right, or does the package need more?",
			Why: "L1 was chosen because it adds no authority. It is a floor, not a finding — " +
				"raising it requires reading the code that the tools come from.",
		},
	}
	if report.Extension > 0 {
		out = append(out, Decision{
			Field:    "extensions/",
			Question: fmt.Sprintf("Do the %d extension(s) run in-process, and do they need credentials to do it?", report.Extension),
			Why: "An extension runs with the node's privileges. Whether it is safe as " +
				"third-party code depends on what it does, not on how it was packaged.",
		})
	}
	if len(result.Warnings) > 0 {
		out = append(out, Decision{
			Field:    "(loader warnings)",
			Question: fmt.Sprintf("%d file(s) in the source did not parse cleanly. What were they?", len(result.Warnings)),
			Why: "A file the loader could not read is a file no review has read either. " +
				"Dropping it silently would leave a package that looks complete.",
		})
	}
	return out
}
