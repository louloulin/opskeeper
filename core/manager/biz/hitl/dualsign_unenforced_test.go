package hitl

import (
	"reflect"
	"strings"
	"testing"

	approvalmodel "github.com/vincent-wuhan/opskeeper/core/manager/model/approval"
	hitlmodel "github.com/vincent-wuhan/opskeeper/core/manager/model/hitl"
)

// TestDualSignCannotBeEnforcedBecauseNowhereStoresTwoSigners is the
// assertion behind this file, and it exists because two pieces of the tree
// said otherwise.
//
// What is true: ADR-019's dual sign is implemented as a validator
// (DualSignPolicy.Validate) that is correct, is unit-tested, and has a
// well-specified rule file. What is also true: nothing calls it. The
// composition root loads the rule file at boot, validates its syntax, and
// prints "hitl: dual sign policy loaded (N rules)" — and then drops the
// policy on the floor, because a local variable going out of scope is the
// only thing that happens to it. This package's own doc comment said the HITL
// web channel calls Validate once the signatures are in. It does not, and it
// could not: the row it would read them from has nowhere to put them.
//
// model.Approval carries ApprovedBy *uint64 and model.Proposal carries
// ApprovedBy *uint64 and ResumedBy *uint64. Three single-valued identity
// columns across the two proposal tables, none of which is a list, so a
// second signer has nowhere to be written. Service.Approve sets
// StatusApproved on its first call and returns.
//
// So the control is not "configured but off". It is absent, and the artefact
// an operator would look at to check on it — a green boot log line — says the
// opposite. That is the specific harm this assertion is here to prevent.
//
// The test is written as a positive statement about the storage rather than a
// negative statement about the code, because the negative one is
// untestable: "nothing calls Validate" is a claim about the whole repository,
// and a test that asserted it would keep passing after the day somebody wires
// it correctly. This one instead watches the shape that makes wiring
// possible. Add a field that can hold two identities and this fails, and its
// message says what has to change with it — the wiring, the boot log line, and
// this package's doc comment.
//
// The second of those three is the one that does the damage, and it is not
// covered by a test here: a test cannot read what main.go logs without running
// the process, so what would have been its assertion is a sentence in this
// comment instead. Writing it as a test — a test that asserts a string
// constant contains its own words — would have been the eighth kind of thing
// this repository keeps refusing: an assertion that cannot fail, wearing the
// costume of a guard.
func TestDualSignCannotBeEnforcedBecauseNowhereStoresTwoSigners(t *testing.T) {
	// These are the fields that record who decided. A list- or map-valued one
	// would be the storage dual sign needs; a scalar is one decision-maker.
	identity := map[string][]reflect.StructField{
		"model/approval.Approval": identityFieldsOf(reflect.TypeOf(approvalmodel.Approval{})),
		"model/hitl.Proposal":     identityFieldsOf(reflect.TypeOf(hitlmodel.Proposal{})),
	}
	for row, fields := range identity {
		if len(fields) == 0 {
			t.Errorf("%s records no decision-maker at all; this test's premise is that it "+
				"records exactly one, and a row that records none cannot be read by any "+
				"validator, single-sign or dual", row)
			continue
		}
		for _, f := range fields {
			if isContainer(f.Type) {
				t.Errorf("%s.%s can hold more than one identity, which means the storage dual "+
					"sign needs now exists. Three things have to happen with it and only the "+
					"first is obvious: wire DualSignPolicy.Validate into the approve path (today "+
					"Service.Approve sets StatusApproved on its first call), make the boot log in "+
					"cmd/opskeeper stop saying the policy is merely loaded, and correct this "+
					"package's doc comment, which claims the web channel calls Validate. Until "+
					"all three are done this test is the thing standing between a schema change "+
					"and a control that is still not enforced",
					row, f.Name)
			}
		}
	}
}

// identityFieldsOf returns the exported fields whose name says they record who
// decided, together with the declared type. Matching on the name rather than
// on a fixed list is deliberate: a new column called Signers or ApprovedBy is
// exactly the change this test is watching for, and a hard-coded field list
// would not see it.
func identityFieldsOf(t reflect.Type) []reflect.StructField {
	var out []reflect.StructField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "approvedby") || strings.Contains(name, "resumedby") ||
			strings.Contains(name, "signer") || strings.Contains(name, "signature") {
			out = append(out, f)
		}
	}
	return out
}

func isContainer(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return true
	default:
		return false
	}
}
