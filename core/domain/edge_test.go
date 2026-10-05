package domain

import (
	"reflect"
	"testing"
)

// TestEdgePresenceCarriesExactlyTheSixColumnsTheTwoCallersRead is the guard the
// projection never had.
//
// Decision 233 measured that `alert` and `systemhealth` together touch six
// columns of a fifteen-column table, and this type is the thing that replaced
// the other nine being reachable. A projection is a promise about what a
// consumer can see, and a promise written only as a struct definition decays
// the first time somebody adds a field because it was convenient — at which
// point the nine that were deliberately left behind start leaking back in one
// at a time, and nobody can tell from the diff which one was the mistake.
//
// So the field set is pinned, by name and by count. Adding a seventh column
// fails here, and the failure is the point: the new column has to be argued
// for rather than slipped in.
func TestEdgePresenceCarriesExactlyTheSixColumnsTheTwoCallersRead(t *testing.T) {
	typ := reflect.TypeOf(EdgePresence{})
	if typ.NumField() != 6 {
		var got []string
		for i := 0; i < typ.NumField(); i++ {
			got = append(got, typ.Field(i).Name)
		}
		t.Fatalf("EdgePresence has %d fields %v, want the 6 the alert staleness gauge and the "+
			"system-health edge probe actually read; every extra column is a column the edge "+
			"domain's credentials, soft-delete and version-self-report stop being able to keep "+
			"to themselves", typ.NumField(), got)
	}
	want := map[string]bool{
		"ID": true, "Name": true, "Status": true,
		"DeviceID": true, "LastSeenAt": true, "CreatedAt": true,
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !want[name] {
			t.Errorf("EdgePresence.%s is not one of the six columns the two callers read; "+
				"if it was added on purpose, update this list in the same commit and say why "+
				"a gauge or a health probe needs it", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("EdgePresence is missing %s, which both callers read; dropping it breaks a "+
			"caller that compiles fine against a zero value", name)
	}
}

// TestTheEdgeStatusConstantsAreTheOnesTheColumnIsConstrainedTo keeps the
// duplicated vocabulary honest.
//
// The edges table carries a CHECK constraint listing 'online' and 'offline',
// and this file repeats both as plain constants so a consumer that only counts
// them does not have to import the model package. That duplication is the
// thing this test exists for: it is a second spelling, and a second spelling
// that drifts is a probe that counts an unreachable status as neither online
// nor offline and reports a fleet as healthier than it is.
func TestTheEdgeStatusConstantsAreTheOnesTheColumnIsConstrainedTo(t *testing.T) {
	if EdgeStatusOnline != "online" {
		t.Errorf("EdgeStatusOnline = %q, want \"online\"; the edges table's CHECK constraint "+
			"allows no other value, so a different constant counts nothing", EdgeStatusOnline)
	}
	if EdgeStatusOffline != "offline" {
		t.Errorf("EdgeStatusOffline = %q, want \"offline\"; same CHECK constraint, same "+
			"consequence", EdgeStatusOffline)
	}
	if EdgeStatusOnline == EdgeStatusOffline {
		t.Error("the two presence states are the same string, so a probe counting them would " +
			"count every node in both buckets")
	}
}
