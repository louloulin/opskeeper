package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree materialises a fixture module and returns its root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const sourcePkg = `package src

type Input struct {
	Alpha   string
	Beta    string
	Gamma   string
	Delta   string
	Epsilon string
	Zeta    string
	Eta     string
	Theta   string
}
`

// The whole tree under test declares one foreign type and one source struct,
// so a test can say what the answer should be rather than what it is.
var tree = map[string]string{
	"src/input.go": sourcePkg,
	"dst/dst.go": `package dst

type Input struct {
	Alpha   string
	Beta    string
	Gamma   string
	Delta   string
	Epsilon string
	Zeta    string
	Eta     string
	Theta   string
}
`,
	"use/full.go": `package use

import dst "example.com/mod/dst"
import "example.com/mod/src"

func Full(in src.Input) dst.Input {
	return dst.Input{Alpha: in.Alpha, Beta: in.Beta, Gamma: in.Gamma, Delta: in.Delta, Epsilon: in.Epsilon, Zeta: in.Zeta, Eta: in.Eta, Theta: in.Theta}
}
`,
	"use/dropped.go": `package use

import dst "example.com/mod/dst"
import "example.com/mod/src"

func Dropped(in src.Input) dst.Input {
	return dst.Input{Alpha: in.Alpha, Beta: in.Beta, Gamma: in.Gamma, Delta: in.Delta, Epsilon: in.Epsilon}
}
`,
	"use/local.go": `package use

// A same-package literal is a declaration or a test fixture, not a copy.
type localThing struct {
	One   string
	Two   string
	Three string
	Four  string
	Five  string
}

func Local(in src.Input) localThing {
	return localThing{One: in.Alpha, Two: in.Beta, Three: in.Gamma, Four: in.Delta, Five: in.Epsilon}
}
`,
	"use/declaration.go": `package use

import dst "example.com/mod/dst"

// Every value is a literal, so this constructs rather than copies.
func Declaration() dst.Input {
	return dst.Input{Alpha: "a", Beta: "b", Gamma: "c", Delta: "d", Epsilon: "e", Zeta: "f"}
}
`,
	"use/narrow.go": `package use

import dst "example.com/mod/dst"
import "example.com/mod/src"

// Three fields is below the threshold of five, and a three-field literal is as
// likely to be a call site as a copy.
func Narrow(in src.Input) dst.Input {
	return dst.Input{Alpha: in.Alpha, Beta: in.Beta, Gamma: in.Gamma}
}
`,
	"use/unresolved.go": `package use

import dst "example.com/mod/dst"
import "example.com/mod/src"

// The receiver is a local, not a parameter, so the source cannot be read.
func Unresolved() dst.Input {
	in := src.Input{Alpha: "a", Beta: "b", Gamma: "c", Delta: "d", Epsilon: "e", Zeta: "f"}
	return dst.Input{Alpha: in.Alpha, Beta: in.Beta, Gamma: in.Gamma, Delta: in.Delta, Epsilon: in.Epsilon, Zeta: in.Zeta, Eta: in.Eta, Theta: in.Theta}
}
`,
}

func analyseTree(t *testing.T) sites {
	t.Helper()
	root := writeTree(t, tree)
	files, index, err := scan([]string{root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return analyse(files, index)
}

func findByFunc(all sites, fn string) *site {
	for _, s := range all {
		if s.fn == fn {
			return s
		}
	}
	return nil
}

// TestAFullTranslationIsReportedAsFull is the case that matters most: a site
// with nothing unset has to be visible in the output, not dropped. A report
// that only lists suspects reads exactly like a report with nothing to say.
func TestAFullTranslationIsReportedAsFull(t *testing.T) {
	s := findByFunc(analyseTree(t), "Full")
	if s == nil {
		t.Fatal("the complete translation was not found at all")
	}
	if !s.resolved || s.sourceFields != 8 || len(s.dropped) != 0 {
		t.Fatalf("a complete translation read as %d/%d with %v unset; it should read 8/8 with none",
			s.sourceFields-len(s.dropped), s.sourceFields, s.dropped)
	}
}

func TestADroppedColumnIsNamed(t *testing.T) {
	s := findByFunc(analyseTree(t), "Dropped")
	if s == nil {
		t.Fatal("the translation with two columns missing was not found")
	}
	if got := strings.Join(s.dropped, ","); got != "Zeta,Eta,Theta" {
		t.Fatalf("unset columns = %q, want Zeta,Eta,Theta", got)
	}
}

func TestALocalLiteralIsNotACopy(t *testing.T) {
	if s := findByFunc(analyseTree(t), "Local"); s != nil {
		t.Fatalf("a same-package literal was counted as a translation: %+v", s)
	}
}

func TestALiteralOfPlainValuesIsADeclaration(t *testing.T) {
	if s := findByFunc(analyseTree(t), "Declaration"); s != nil {
		t.Fatalf("a literal whose values are all constants was counted as a copy: %+v", s)
	}
}

func TestANarrowLiteralIsBelowTheThreshold(t *testing.T) {
	if s := findByFunc(analyseTree(t), "Narrow"); s != nil {
		t.Fatalf("a %d-field literal was counted as a translation: %+v", minFields-2, s)
	}
}

// TestAnUnresolvedSiteIsAbsentRatherThanClean is the one that keeps the
// report honest. A site the analysis cannot read must not appear in the
// resolved list, and the output has to say so in words — otherwise a reader
// takes "not in the list" for "checked and fine".
func TestAnUnresolvedSiteIsAbsentRatherThanClean(t *testing.T) {
	all := analyseTree(t)
	s := findByFunc(all, "Unresolved")
	if s == nil {
		t.Fatal("the unresolved translation was not found at all, so it is not counted as a gap either")
	}
	if s.resolved {
		t.Fatal("a local receiver was resolved; the claim that resolution needs a parameter is wrong")
	}
	var buf strings.Builder
	all.print(writerOf(&buf))
	out := buf.String()
	if !strings.Contains(out, "did NOT resolve") {
		t.Error("the report does not say that some sites went unread")
	}
	if !strings.Contains(out, "which is not the") {
		t.Error("the report does not say that an unread site is not a clean site")
	}
}

type stringWriter struct{ b *strings.Builder }

func (w stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func writerOf(b *strings.Builder) *stringWriter { return &stringWriter{b: b} }

// TestTheKnownMissesArePrinted pins the false-positive classes into the
// output. They were found by reading the sites, and an enumeration that only
// lives in a commit message stops existing the next time somebody reorders
// this file.
func TestTheKnownMissesArePrinted(t *testing.T) {
	var buf strings.Builder
	sites{}.print(writerOf(&buf))
	out := buf.String()
	for _, k := range knownFalsePositives {
		if !strings.Contains(out, k.kind) {
			t.Errorf("the %s class is not named in the report; it is one of the ways this command is wrong", k.kind)
		}
		if !strings.Contains(out, k.where) {
			t.Errorf("the %s class does not name the site it was found at (%s)", k.kind, k.where)
		}
	}
}

// TestTheEmptyRunStillExplainsItself: a report over a tree with no
// translations has to say that, rather than printing nothing and reading as
// a clean bill.
func TestTheEmptyRunStillExplainsItself(t *testing.T) {
	var buf strings.Builder
	sites{}.print(writerOf(&buf))
	out := buf.String()
	if !strings.Contains(out, "no site resolved its source struct") {
		t.Error("an empty run does not say that it measured nothing")
	}
}
