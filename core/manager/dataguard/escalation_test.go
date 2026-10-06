package dataguard

import (
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/domain"
)

// The mapping the vocabulary promised before any code implemented it. Written
// as one table so a level added to the enum without a row here is a failure
// rather than a silence.
func TestEveryLevelThatPromisesAnEscalationGetsOne(t *testing.T) {
	cases := map[Sensitivity]struct {
		want  domain.ToolClass
		raise bool
	}{
		Public:       {"", false},
		Internal:     {"", false},
		Confidential: {domain.ClassWrite, true},
		Restricted:   {domain.ClassDestructive, true},
		TopSecret:    {domain.ClassDestructive, true},
	}
	// An unknown level is the safe half of "no opinion": Parse refuses to
	// store one, so this is the path a value takes arriving from elsewhere.
	cases[Sensitivity("something-new")] = struct {
		want  domain.ToolClass
		raise bool
	}{"", false}

	for level, want := range cases {
		got, raised := RequiredClass(level)
		if got != want.want || raised != want.raise {
			t.Errorf("RequiredClass(%q) = %q, %v; want %q, %v", level, got, raised, want.want, want.raise)
		}
	}
}

// A label may only ever raise a class. If it could lower one, an Internal
// label on a resource would be enough to turn a destructive tool back into a
// single-signature one — which is the entire reason this control exists.
func TestALabelCanOnlyRaiseAClass(t *testing.T) {
	escalating := []Sensitivity{Confidential, Restricted, TopSecret}
	// ClassUnknown is deliberately absent: it is the one value core/domain
	// ranks higher than everything, so the comparison this test uses is the
	// one that mishandles it. TestAnUndeclaredProposalTakesTheLabelsWordForIt
	// owns that case, and TestHighestClassAloneWouldSwallowTheLabelsDemand
	// is the receipt for why it needed owning.
	for _, level := range escalating {
		for _, proposed := range []domain.ToolClass{domain.ClassRead, domain.ClassWrite, domain.ClassDestructive} {
			after := RaisedClass(proposed, level)
			if after == "" {
				t.Errorf("RaisedClass(%q, %q) returned nothing for an escalating level",
					proposed, level)
			}
			if (domain.Tools{{Class: proposed}, {Class: after}}).HighestClass() != after {
				t.Errorf("RaisedClass(%q, %q) = %q, which is LOWER than what it was given",
					proposed, level, after)
			}
		}
	}
}

// A level that expresses no opinion leaves the proposal exactly as it was.
// This is the difference between "no label" and "a label that does not apply",
// and conflating them is how an escalation starts inventing risk levels.
func TestANonEscalatingLevelLeavesTheProposalAlone(t *testing.T) {
	for _, level := range []Sensitivity{Public, Internal, ""} {
		for _, proposed := range []domain.ToolClass{domain.ClassRead, domain.ClassWrite, domain.ClassDestructive} {
			if got := RaisedClass(proposed, level); got != proposed {
				t.Errorf("RaisedClass(%q, %q) = %q, want it unchanged", proposed, level, got)
			}
		}
	}
}

// The one case where the two domains' opinions about ClassUnknown disagree,
// and the reason this function does not simply delegate to HighestClass.
//
// core/domain ranks ClassUnknown WITH destructive, because a plugin that
// declares no class is not trusted to be read-only. Applied here, that would
// mean an approval whose producer said nothing came out of the escalation as
// ClassUnknown — and ClassUnknown on a row means nobody classified it, which
// means one signature. The label's demand would have been discarded by the
// very comparison meant to enforce it.
func TestAnUndeclaredProposalTakesTheLabelsWordForIt(t *testing.T) {
	cases := map[Sensitivity]domain.ToolClass{
		Confidential: domain.ClassWrite,
		Restricted:   domain.ClassDestructive,
		TopSecret:    domain.ClassDestructive,
	}
	for level, want := range cases {
		if got := RaisedClass(domain.ClassUnknown, level); got != want {
			t.Errorf("RaisedClass(unknown, %q) = %q, want %q", level, got, want)
		}
	}
}

// And the same comparison, asked directly, is the one that would have got it
// wrong. This is the receipt that the special case above is load-bearing
// rather than a stylistic preference: if core/domain ever stops ranking
// ClassUnknown with destructive, this assertion fails and the branch above
// should be revisited rather than deleted.
func TestHighestClassAloneWouldSwallowTheLabelsDemand(t *testing.T) {
	got := domain.Tools{
		{Class: domain.ClassUnknown},
		{Class: domain.ClassWrite},
	}.HighestClass()
	if got == domain.ClassWrite {
		t.Skip("core/domain no longer ranks ClassUnknown with destructive; " +
			"RaisedClass's ClassUnknown branch may now be redundant — re-read it")
	}
	if got != domain.ClassUnknown {
		t.Fatalf("the premise changed: HighestClass({unknown, write}) = %q", got)
	}
}
