package pirpc

import (
	"strings"
	"testing"
)

func TestArgsUsesRPCModeNotHTTP(t *testing.T) {
	args := LaunchOptions{Script: "/opt/pi/cli.js"}.Args()
	joined := strings.Join(args, " ")
	if !strings.HasPrefix(joined, "--mode rpc") {
		t.Fatalf("args must start with --mode rpc: %q", joined)
	}
	// Regression guard for the plan1.0 argv that Pi rejects.
	for _, forbidden := range []string{"--mode http", "--bind", "--port", "--skills-root"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("args contain unsupported flag %q: %q", forbidden, joined)
		}
	}
}

func TestCommandSplitsNodeAndScript(t *testing.T) {
	bin, args, err := LaunchOptions{
		Node:   "/usr/bin/node",
		Script: "vendor/pi/packages/coding-agent/dist/bundle/cli.js",
	}.Command()
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if bin != "/usr/bin/node" {
		t.Fatalf("bin = %q", bin)
	}
	if args[0] != "vendor/pi/packages/coding-agent/dist/bundle/cli.js" {
		t.Fatalf("script must be argv[1]: %v", args)
	}
	if args[1] != "--mode" || args[2] != "rpc" {
		t.Fatalf("flags must follow the script path: %v", args)
	}
}

func TestCommandDefaultsNodeInterpreter(t *testing.T) {
	bin, _, err := LaunchOptions{Script: "cli.js"}.Command()
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if bin != "node" {
		t.Fatalf("bin = %q, want node", bin)
	}
}

func TestCommandRejectsCommandLineAsScript(t *testing.T) {
	// This is the shape the previous OPSKEEPER_PI_BIN default had:
	// "node vendor/pi/.../cli.js" in a single string can never be
	// exec'd.
	_, _, err := LaunchOptions{Script: "node vendor/pi/packages/coding-agent/dist/cli.js"}.Command()
	if err == nil {
		t.Fatal("want error for a command line passed as Script")
	}
	if !strings.Contains(err.Error(), "not a path") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestCommandRejectsBinAndScriptTogether(t *testing.T) {
	if _, _, err := (LaunchOptions{Bin: "pi", Script: "cli.js"}).Command(); err == nil {
		t.Fatal("want error when both Bin and Script are set")
	}
}

func TestCommandRequiresTarget(t *testing.T) {
	if _, _, err := (LaunchOptions{}).Command(); err == nil {
		t.Fatal("want error with neither Bin nor Script")
	}
}

func TestCommandUsesBinDirectly(t *testing.T) {
	bin, args, err := LaunchOptions{Bin: "/usr/local/bin/pi"}.Command()
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if bin != "/usr/local/bin/pi" || args[0] != "--mode" {
		t.Fatalf("bin=%q args=%v", bin, args)
	}
}

func TestArgsRendersHardeningFlags(t *testing.T) {
	args := LaunchOptions{
		Script:            "cli.js",
		Provider:          "openai",
		Model:             "gpt-4o-mini",
		SystemPromptFile:  "/etc/opskeeper/pi/SYSTEM.md",
		SkillDirs:         []string{"/opt/opskeeper/pi-skills", ""},
		Tools:             []string{"read", "grep", "find", "ls"},
		ExcludeTools:      []string{"ask_question"},
		SessionName:       "edge-1 rca",
		NoExtensions:      true,
		NoSkills:          true,
		NoPromptTemplates: true,
		NoContextFiles:    true,
		Offline:           true,
		Ephemeral:         true,
	}.Args()
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"--mode rpc",
		"--provider openai",
		"--model gpt-4o-mini",
		"--append-system-prompt /etc/opskeeper/pi/SYSTEM.md",
		"--skill /opt/opskeeper/pi-skills",
		"--tools read,grep,find,ls",
		"--exclude-tools ask_question",
		"--no-session",
		"--name edge-1 rca",
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-context-files",
		"--offline",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	// An empty skill dir must not emit a bare --skill.
	if strings.Contains(joined, "--skill --tools") {
		t.Fatalf("empty skill dir emitted: %q", joined)
	}
}

func TestEphemeralWinsOverSessionDir(t *testing.T) {
	args := strings.Join(LaunchOptions{
		Script:     "cli.js",
		Ephemeral:  true,
		SessionDir: "/var/lib/opskeeper/pi-sessions",
	}.Args(), " ")
	if strings.Contains(args, "--session-dir") {
		t.Fatalf("--no-session and --session-dir are mutually exclusive: %q", args)
	}
	if !strings.Contains(args, "--no-session") {
		t.Fatalf("missing --no-session: %q", args)
	}
}

func TestSessionDirUsedWhenPersistent(t *testing.T) {
	args := strings.Join(LaunchOptions{
		Script:     "cli.js",
		SessionDir: "/var/lib/opskeeper/pi-sessions",
	}.Args(), " ")
	if !strings.Contains(args, "--session-dir /var/lib/opskeeper/pi-sessions") {
		t.Fatalf("missing --session-dir: %q", args)
	}
}
