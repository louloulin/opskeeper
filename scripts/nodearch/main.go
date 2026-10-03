// Command nodearch checks that the node binaries sitting in bin/<os>-<arch>/
// are actually built for that operating system and architecture.
//
// The delivery chain already gets the directory right — dist/build-edge-bundle.sh
// derives its source directory from the arch argument it is handed, and the
// four Makefile targets each spell out their own GOOS/GOARCH. What no part of
// the repository checks is the artefact inside that directory. Cross-compiling
// four targets from one Makefile is four chances to write a host build into a
// cross slot, and a host build in a target slot is invisible until a customer
// node tries to exec it: not a crash, not a log line, just ENOEXEC on a
// machine nobody in the project runs.
//
// The check reads `go version -m`, which reports the GOOS/GOARCH/CGO_ENABLED
// the toolchain recorded in the binary itself. That is the artefact's own
// account of how it was built, which is the only account that can contradict
// the filename.
//
// It also compares each edge against the agent that edge spawns. Two binaries
// in the right directories can still disagree with each other, and a node
// whose edge cannot exec its agent starts, authenticates, answers "how are
// you" and has no tools.
//
// Usage:
//
//	go run ./scripts/nodearch [repo-root]
//
// Every violation is reported, not just the first. Exit status is 1 if any
// rule was violated.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	goBin := os.Getenv("GO")
	if goBin == "" {
		goBin = "go"
	}

	binRoot := filepath.Join(root, "bin")
	result := run(root, binRoot, goBin)
	result.Report(os.Stdout)
	if !result.OK() {
		os.Exit(1)
	}
}

// run walks the four target directories and judges what it finds there.
func run(root, binRoot, goBin string) *Result {
	res := &Result{}
	for _, target := range Targets() {
		infos := map[string]BuildInfo{}
		read := map[string]bool{}
		for _, slot := range SlotsFor(binRoot, target) {
			path := slot.Abs
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)

			// Existence is decided with stat rather than by reading the
			// exec error. A missing go toolchain and a missing binary
			// both surface as ENOENT from Start, and conflating them
			// would let a broken environment report four targets as
			// "not built" — a build problem — instead of "not checked".
			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					if slot.Required {
						res.Findings = append(res.Findings, Finding{
							Binary: rel,
							Rule:   ruleMissing,
							Detail: "required node binary absent; a node without it starts and has no AI agent at all",
						})
					} else {
						res.Skipped = append(res.Skipped, Skipped{Binary: rel, Reason: slot.Reason})
					}
					continue
				}
				res.Findings = append(res.Findings, Finding{
					Binary: rel, Rule: ruleUnreadable, Detail: err.Error(),
				})
				continue
			}
			info, err := readBuildInfo(goBin, path)
			if err != nil {
				// An unreadable binary is not a pass. Reporting it keeps a
				// broken toolchain — or a truncated artefact — from reading
				// as four targets verified.
				res.Findings = append(res.Findings, Finding{
					Binary: rel, Rule: ruleUnreadable, Detail: err.Error(),
				})
				continue
			}
			read[filepath.Base(path)] = true
			infos[filepath.Base(path)] = info
			res.Checked++
			res.Findings = append(res.Findings, CheckOne(rel, target, info)...)
		}
		edge, agent := Edge, Agent
		if read[edge] && read[agent] {
			res.CheckedPairs++
			relEdge, _ := filepath.Rel(root, filepath.Join(binRoot, target.String(), edge))
			res.Findings = append(res.Findings,
				CheckPair(filepath.ToSlash(relEdge), agent, infos[edge], infos[agent])...)
		}
	}
	return res
}

// readBuildInfo asks the Go toolchain to describe a binary.
//
// `go version -m` reads the build info the compiler embedded, so it works on
// any Go binary regardless of the host it is inspected from — which is what
// makes it usable for cross-compiled targets at all. It needs a Go toolchain
// to be installed, though, which is why a failure here is a finding and not a
// skip.
func readBuildInfo(goBin, path string) (BuildInfo, error) {
	out, err := exec.Command(goBin, "version", "-m", path).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return BuildInfo{}, fmt.Errorf("cannot read build info: %s", firstLine(detail))
	}
	return ParseBuildInfo(string(out)), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
