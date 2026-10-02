package nodefleet

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Conversation limits are the difference between a fleet that is large and a
// manager that has a bad afternoon. Everything here is about the two ways a
// cap can be written so that it does not actually cap anything.

func openOn(t *testing.T, f *Fleet, edge uint64, session string) error {
	t.Helper()
	_, err := f.Open(PromptRequest{EdgeID: edge, SessionID: session}, &recordingSink{})
	return err
}

func TestThePerNodeCapRefusesTheOneAfterIt(t *testing.T) {
	f, err := New(Options{Dial: &fakeDial{reply: acceptAll()}, MaxSessionsPerEdge: 3, MaxSessions: 100})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(f.CloseAll)

	for i := 0; i < 3; i++ {
		if err := openOn(t, f, 7, fmt.Sprintf("s-%d", i)); err != nil {
			t.Fatalf("conversation %d within the cap: %v", i, err)
		}
	}
	err = openOn(t, f, 7, "s-over")
	if err == nil {
		t.Fatal("the fourth conversation on one node was accepted")
	}
	if !errors.Is(err, ErrFleetFull) {
		t.Errorf("err = %v, want it to unwrap to ErrFleetFull so a caller can branch on it", err)
	}
	var limit *LimitError
	if !errors.As(err, &limit) {
		t.Fatalf("err = %v, want a *LimitError carrying the numbers the operator needs", err)
	}
	if limit.Scope != "edge" || limit.EdgeID != 7 || limit.Open != 3 || limit.Limit != 3 {
		t.Errorf("LimitError = %+v, want edge 7 at 3 of 3", limit)
	}
	if !strings.Contains(limit.Error(), "edge 7") {
		t.Errorf("message %q does not name the node, so the operator cannot tell which one is full", limit.Error())
	}
	if got := f.SessionCount(); got != 3 {
		t.Errorf("SessionCount = %d, want 3: a refused conversation must leave nothing behind", got)
	}
}

func TestThePerNodeCapIsPerNode(t *testing.T) {
	// A cap that leaked across nodes would make one busy node's operators
	// unable to start work on a quiet one, which is the opposite of what a
	// per-node cap is for.
	f, err := New(Options{Dial: &fakeDial{reply: acceptAll()}, MaxSessionsPerEdge: 2, MaxSessions: 100})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(f.CloseAll)

	for edge := uint64(1); edge <= 3; edge++ {
		for i := 0; i < 2; i++ {
			if err := openOn(t, f, edge, fmt.Sprintf("s-%d", i)); err != nil {
				t.Fatalf("edge %d conversation %d: %v", edge, i, err)
			}
		}
	}
	if err := openOn(t, f, 3, "s-over"); err == nil {
		t.Error("the cap did not apply on the third node")
	}
	if err := openOn(t, f, 4, "s-0"); err != nil {
		t.Errorf("a fourth node was refused by another node's cap: %v", err)
	}
}

func TestTheFleetCapAppliesAcrossNodes(t *testing.T) {
	f, err := New(Options{Dial: &fakeDial{reply: acceptAll()}, MaxSessionsPerEdge: 10, MaxSessions: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(f.CloseAll)

	for i := 0; i < 4; i++ {
		if err := openOn(t, f, uint64(i+1), "s-0"); err != nil {
			t.Fatalf("conversation %d within the fleet cap: %v", i, err)
		}
	}
	// One per node, so the per-edge cap of 10 is nowhere near reached: only
	// the fleet cap can be what refuses this.
	err = openOn(t, f, 5, "s-0")
	if err == nil {
		t.Fatal("the fleet accepted a fifth conversation under a cap of 4")
	}
	var limit *LimitError
	if !errors.As(err, &limit) || limit.Scope != "fleet" {
		t.Fatalf("err = %v, want a fleet-scoped LimitError; a per-edge cap of 10 was not reached", err)
	}
	if limit.Open != 4 || limit.Limit != 4 {
		t.Errorf("LimitError = %+v, want 4 of 4", limit)
	}
}

// The regression this whole file is about. A cap checked before taking the
// lock is a cap that holds in every test and not in production: two consoles
// reading "2 of 2" at the same instant both insert, and the limit is exceeded
// by however many goroutines happened to collide.
func TestTheCapHoldsUnderConcurrentOpens(t *testing.T) {
	const cap = 8
	const attempts = 64
	f, err := New(Options{Dial: &fakeDial{reply: acceptAll()}, MaxSessionsPerEdge: cap, MaxSessions: cap})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(f.CloseAll)

	var opened atomic.Int64
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < attempts; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			if err := openOn(t, f, 7, fmt.Sprintf("s-%d", i)); err == nil {
				opened.Add(1)
			}
		}(i)
	}
	start.Done()
	done.Wait()

	if got := opened.Load(); got != cap {
		t.Errorf("%d of %d concurrent opens succeeded under a cap of %d", got, attempts, cap)
	}
	if got := f.SessionCount(); got != cap {
		t.Errorf("SessionCount = %d, want %d", got, cap)
	}
}

// A cap that never reopens is not a cap, it is a shutdown. Closing a
// conversation has to return its slot, or an operator who works through a
// shift eventually cannot start anything at all.
func TestClosingAConversationMakesRoomAgain(t *testing.T) {
	f, err := New(Options{Dial: &fakeDial{reply: acceptAll()}, MaxSessionsPerEdge: 1, MaxSessions: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(f.CloseAll)

	if err := openOn(t, f, 7, "s-1"); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := openOn(t, f, 7, "s-2"); err == nil {
		t.Fatal("the second conversation was accepted under a cap of 1")
	}
	f.Close(7, "s-1")
	if err := openOn(t, f, 7, "s-2"); err != nil {
		t.Errorf("a closed conversation did not release its slot: %v", err)
	}
}

func TestZeroCapsSelectTheDefaults(t *testing.T) {
	f, err := New(Options{Dial: &fakeDial{reply: acceptAll()}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(f.CloseAll)

	perEdge, total := f.Limits()
	if perEdge != DefaultMaxSessionsPerEdge || total != DefaultMaxSessionsTotal {
		t.Errorf("Limits = (%d, %d), want the defaults (%d, %d)",
			perEdge, total, DefaultMaxSessionsPerEdge, DefaultMaxSessionsTotal)
	}
}

func TestANegativeCapIsRefused(t *testing.T) {
	// -1 is how "unlimited" gets typed into a config file by someone who
	// assumed it was supported. Failing here says so once, at startup,
	// instead of at the first conversation on the busiest node.
	if _, err := New(Options{Dial: &fakeDial{reply: acceptAll()}, MaxSessionsPerEdge: -1}); err == nil {
		t.Error("a negative per-node cap was accepted")
	}
	if _, err := New(Options{Dial: &fakeDial{reply: acceptAll()}, MaxSessions: -1}); err == nil {
		t.Error("a negative fleet cap was accepted")
	}
}
