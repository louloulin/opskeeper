package dataguard

import "github.com/vincent-wuhan/opskeeper/core/domain"

// RequiredClass is the tool class a resource's label demands.
//
// This is the mapping the vocabulary has been promising since before any code
// implemented it, and the reason it lives here rather than in the hitl
// package that used to hold a copy is that two copies of a security mapping
// are one copy too many: the day they disagree, one of them is the one an
// operator is reading.
//
// The direction is deliberately one-way and deliberately loud. A label can
// only ever raise the class, never lower it, so a resource nobody has
// classified cannot make an action look safer than the tool that proposed it,
// and a mislabelled Internal cannot suppress the escalation a Restricted
// resource deserves.
//
// The thresholds:
//
//	Public / Internal   → no opinion; the tool's own class stands
//	Confidential        → write      (a read is fine; a change needs a human)
//	Restricted          → destructive
//	TopSecret           → destructive, and the row is unreadable anyway
//
// The last clause is the one that matters most: TopSecret is not "destructive
// if you catch it", it is a resource whose reader tier nobody holds by
// accident, so the write is the least of what the label buys.
func RequiredClass(s Sensitivity) (domain.ToolClass, bool) {
	switch s {
	case Confidential:
		return domain.ClassWrite, true
	case Restricted, TopSecret:
		return domain.ClassDestructive, true
	default:
		// Public, Internal, and anything unparseable. "Anything
		// unparseable" is the safe half of this: an unknown level is
		// treated as no opinion rather than as the highest one, because
		// Parse already refuses to store one and this is the path a value
		// takes when it arrives from somewhere other than the store.
		return "", false
	}
}

// RaisedClass returns the stricter of a proposed class and the class a label
// demands. It is the one function callers should use; RequiredClass is
// exported for the places that need the label's opinion on its own.
func RaisedClass(proposed domain.ToolClass, s Sensitivity) domain.ToolClass {
	required, ok := RequiredClass(s)
	if !ok {
		return proposed
	}
	// ClassUnknown is handled apart from the ranking, and it has to be.
	//
	// core/domain ranks it WITH destructive on purpose: a plugin that does
	// not declare its class is not trusted to be read-only. That is the
	// right rule for admitting a package, and borrowing it here would invert
	// the control — "the producer said nothing" would outrank "the label
	// says Restricted", so an unclassified proposal on a Restricted resource
	// would come out of this function as ClassUnknown, and ClassUnknown on an
	// approval row means nobody classified it, which means one signature.
	//
	// So an undeclared proposal takes the label's word for it.
	if proposed == domain.ClassUnknown {
		return required
	}
	// Everything else goes through core/domain's ordering, next to the
	// constants it orders, so a new level between write and destructive
	// cannot be added without the comparison following it.
	return domain.Tools{
		{Class: proposed},
		{Class: required},
	}.HighestClass()
}
