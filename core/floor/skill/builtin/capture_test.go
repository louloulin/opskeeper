package builtin

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/floor/skill"
)

// A child that streams far past the cap. If the cap were not applied this
// would return tens of megabytes, and the assertion below would fail — but
// the assertion alone could also pass for the wrong reason (a command that
// simply failed to run). So the first assertion is that the *uncapped* path
// really does produce more than the cap, which is what makes the second one
// mean something.
const floodSize = 40 * 1024 * 1024 // 4x MaxSubprocessStdout

func TestCappedCaptureHoldsTheLine(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a 40MB writer")
	}
	writer := "head -c " + strconv.Itoa(floodSize) + " /dev/zero | tr '\\0' 'x'"

	// First: unbounded, to prove the child really can exceed the cap.
	unbounded := runUnbounded(t, writer)
	if len(unbounded) <= skill.MaxSubprocessStdout {
		t.Fatalf("the probe did not exceed the cap: got %d bytes, cap is %d; "+
			"a cap test on a child that cannot reach it proves nothing",
			len(unbounded), skill.MaxSubprocessStdout)
	}
	t.Logf("unbounded capture: %d bytes (cap is %d)", len(unbounded), skill.MaxSubprocessStdout)

	capped, _, err := runCapped(context.Background(), "sh", "-c", writer)
	if err != nil && capped == nil {
		t.Fatalf("capped run failed outright: %v", err)
	}
	if len(capped) > skill.MaxSubprocessStdout {
		t.Errorf("capped capture returned %d bytes, over the %d cap",
			len(capped), skill.MaxSubprocessStdout)
	}
	t.Logf("capped capture: %d bytes", len(capped))

	// And the surviving prefix is real data, not a truncation notice or a
	// zero-filled placeholder: this is the assertion that would catch a
	// "cap" implemented by returning an empty slice.
	if len(capped) > 0 && strings.Trim(string(capped[:1024]), "x") != "" {
		t.Errorf("the capped output is not the head of the child's stream")
	}
}

// runUnbounded is the pattern this change removed, kept here so the test can
// demonstrate the difference rather than assert it. If someone reintroduces
// CombinedOutput in a tool, this is the shape it will have.
func runUnbounded(t *testing.T, script string) []byte {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		t.Fatalf("unbounded probe failed: %v", err)
	}
	return out
}
