package webshell

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	devicemodel "github.com/vincent-wuhan/opskeeper/core/manager/model/device"
	edgemodel "github.com/vincent-wuhan/opskeeper/core/manager/model/edge"
)

// fleetBeyondTheOldPage is the number this whole file is about.
//
// The implementation this replaced asked the edge store for the newest
// thousand edges and looked for the one belonging to the device in Go. A
// fleet of a thousand and one therefore produced "device offline or unknown"
// about a host that was answering heartbeats, and the operator's next move
// was to go and reboot a machine that was fine.
//
// A regression test for that has to put the target past the old page, or it
// passes against the old code and proves nothing. Everything below is
// therefore anchored to a device whose edge id is beyond it.
const fleetBeyondTheOldPage = 1000

type fakeLinks struct {
	edgeID uint64
	err    error
	calls  int
	seen   []uint64
}

func (f *fakeLinks) LookupEdgeForDevice(_ context.Context, deviceID uint64, t devicemodel.EdgeDeviceRelationType) (uint64, error) {
	f.calls++
	f.seen = append(f.seen, deviceID)
	if t != devicemodel.EdgeDeviceRelationHost {
		return 0, fmt.Errorf("webshell asked for relation %d, which is not the host relation", t)
	}
	return f.edgeID, f.err
}

type fakeEdgeStatus struct {
	edge  *edgemodel.Edge
	err   error
	calls int
}

func (f *fakeEdgeStatus) GetByID(_ context.Context, id uint64) (*edgemodel.Edge, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.edge == nil {
		return nil, errors.New("edge row is gone")
	}
	if f.edge.ID != id {
		return nil, fmt.Errorf("asked for edge %d, store holds %d", id, f.edge.ID)
	}
	return f.edge, nil
}

func newHandlerUnderTest(links DeviceLinks, edges EdgeStatusLookup) *Handler {
	return &Handler{links: links, edges: edges}
}

// TestADeviceBeyondTheOldPageStillResolves is the regression this file exists
// for. The old code could not pass it: it had no way to ask about edge 4321
// without listing a page, and the page stopped at 1000.
func TestADeviceBeyondTheOldPageStillResolves(t *testing.T) {
	const (
		deviceID = uint64(77)
		edgeID   = uint64(fleetBeyondTheOldPage + 341)
	)
	links := &fakeLinks{edgeID: edgeID}
	edges := &fakeEdgeStatus{edge: &edgemodel.Edge{ID: edgeID, Status: edgemodel.StatusOnline}}

	got, err := newHandlerUnderTest(links, edges).resolveEdge(context.Background(), deviceID)
	if err != nil {
		t.Fatalf("resolveEdge: %v", err)
	}
	if got != edgeID {
		t.Fatalf("resolved edge %d, want %d", got, edgeID)
	}
}

// TestResolvingADeviceCostsTwoPointLookups pins the property that makes the
// bug impossible rather than merely absent: the answer no longer depends on
// how many edges exist, because nothing lists them.
//
// Counting the calls is the assertion. A test that only checks the returned
// id would still be green against an implementation that lists the whole
// table and filters it correctly, which is the shape that was wrong.
func TestResolvingADeviceCostsTwoPointLookups(t *testing.T) {
	links := &fakeLinks{edgeID: 9}
	edges := &fakeEdgeStatus{edge: &edgemodel.Edge{ID: 9, Status: edgemodel.StatusOnline}}

	if _, err := newHandlerUnderTest(links, edges).resolveEdge(context.Background(), 3); err != nil {
		t.Fatalf("resolveEdge: %v", err)
	}
	if links.calls != 1 {
		t.Errorf("junction looked up %d times, want 1: the device-to-edge relation is a single row", links.calls)
	}
	if edges.calls != 1 {
		t.Errorf("edge status read %d times, want 1: it is a primary key read", edges.calls)
	}
}

// TestTheEdgePortStaysOneMethodWide stops the fix from being undone by
// widening. Nothing stops somebody handing this handler the full edge
// repository again, and the full repository has a List on it — at which
// point the page comes back, and with it the fleet-size cliff.
//
// A method count is an odd thing to assert, and it is asserted here because
// the alternative is a comment, and a comment is what the next person
// optimises away.
func TestTheEdgePortStaysOneMethodWide(t *testing.T) {
	got := reflect.TypeOf((*EdgeStatusLookup)(nil)).Elem()
	if got.NumMethod() != 1 {
		t.Fatalf("EdgeStatusLookup has %d methods (%v); a second one is how List comes back, "+
			"and List is what made a fleet of a thousand and one look like a dead host",
			got.NumMethod(), methodNames(got))
	}
	if name := got.Method(0).Name; name != "GetByID" {
		t.Errorf("EdgeStatusLookup's one method is %s, want GetByID", name)
	}
}

func methodNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		out = append(out, t.Method(i).Name)
	}
	return out
}

// TestTheThreeWaysAShellCannotOpenSayDifferentThings is about the log, not
// the wire. The browser still gets one 503 — that contract is unchanged and
// a client that only knows "not now" is easier to serve than one that has to
// understand our reasons. But the three reasons are genuinely different
// incidents: a host nobody registered, a host whose agent died, and a row
// that was deleted out from under a live session. An operator reading the
// log needs to be able to tell them apart, and "device offline or unknown"
// told them nothing.
func TestTheThreeWaysAShellCannotOpenSayDifferentThings(t *testing.T) {
	cases := []struct {
		name  string
		links *fakeLinks
		edges *fakeEdgeStatus
		wants []string
	}{
		{
			name:  "no edge was ever registered for the device",
			links: &fakeLinks{err: errors.New("edge_devices: not found")},
			edges: &fakeEdgeStatus{edge: &edgemodel.Edge{ID: 1, Status: edgemodel.StatusOnline}},
			wants: []string{"no edge registered", "not found"},
		},
		{
			name:  "the junction row points at nothing",
			links: &fakeLinks{edgeID: 0},
			edges: &fakeEdgeStatus{edge: &edgemodel.Edge{ID: 1, Status: edgemodel.StatusOnline}},
			wants: []string{"no edge registered"},
		},
		{
			name:  "the agent is registered but not answering",
			links: &fakeLinks{edgeID: 12},
			edges: &fakeEdgeStatus{edge: &edgemodel.Edge{ID: 12, Status: edgemodel.StatusOffline}},
			wants: []string{"is offline"},
		},
		{
			name:  "the edge row disappeared",
			links: &fakeLinks{edgeID: 12},
			edges: &fakeEdgeStatus{err: errors.New("record not found")},
			wants: []string{"read edge 12"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := newHandlerUnderTest(tc.links, tc.edges).resolveEdge(context.Background(), 5)
			if err == nil {
				t.Fatalf("resolveEdge returned edge %d, want an error", id)
			}
			if id != 0 {
				t.Errorf("resolveEdge returned edge id %d alongside an error; a caller that "+
					"checks the id first would open a stream to an edge it could not verify", id)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q; the three failures are different "+
						"incidents and the log is the only place they can be told apart", err, want)
				}
			}
		})
	}
}

// TestAnUnwiredLookupRefusesRatherThanGuessing covers the boot-time mistake.
// A nil dependency used to sit in a struct field that nothing read, so a
// miswiring was invisible until something else broke. Now the field is on
// the path, and the only safe answer to "nobody told me how to find the
// edge" is to refuse.
func TestAnUnwiredLookupRefusesRatherThanGuessing(t *testing.T) {
	if _, err := (&Handler{}).resolveEdge(context.Background(), 1); err == nil {
		t.Fatal("a handler with no lookups wired resolved a device; it must refuse instead")
	}
}
