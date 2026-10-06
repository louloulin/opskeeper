package main

import (
	"os"
	"path/filepath"
	"testing"
)

// unreachableBudget is the number this tree is allowed to have and no more.
//
// It is a ratchet rather than a threshold, and the difference is the whole
// point. A threshold says "823 is too many" and is therefore always false —
// the number has been near 800 for thirty decisions and the right answer about
// any given one of them was "leave it, it is accounted for". A ratchet says
// "823 today, and tomorrow you either go down or you say out loud why you
// went up". The second one is the only one that changes what happens, because
// the cost lands on the person adding the symbol rather than on whoever reads
// the report last.
//
// The two class counts are pinned too, and not because the gate needs them —
// the gate trips on the total, so that a symbol moving from dead to test-only
// (which happens the moment someone writes a test for it) does not read as
// growth. They are pinned because the finding that produced this file was in
// one of the two classes, and a total cannot tell a reader which one grew. A
// ratchet on a number nobody can decompose is a number nobody acts on.
// 876 / 608 / 268 as of decision 338: the three report_task_* actions plus the
// report_task resource type, same re-export category as every line below and
// as decision 337 immediately above. Raised in the same commit as the rows that
// read them — the constants are dead *from inside core/base* because their only
// readers are the report handlers in core/manager.
//
// 872 / 604 / 268 as of decision 337: the eight report/schedule actions plus
// two resource types, all re-exported from core/base into core/domains the way
// every line below is. They read as dead **from inside the core/base tree**
// because their only readers are in another module — the same category as
// decisions 311, 312, 335 and 336, and raised in the same commit as the change
// that caused the growth. The 604 is dead-code growth rather than test-only
// growth, which is the signature of a vocabulary written ahead of its readers;
// this one is not speculative, because the eight rows went onto the chain in
// this same commit.
//
// 862 / 594 / 268 as of decision 336: the four ssh_key_* actions.
//
// Worth noting what did *not* happen here: this decision needed a resource type
// and did not add one, because `ResourceGitKey` had been declared since the
// vocabulary was first written down and never used by anybody. **A closed
// vocabulary accumulates the names of things nobody has built yet** — the
// resource type was written down in anticipation of exactly this surface, and
// four decisions later it was still dead. Reusing it is the whole reason this
// line is +4 rather than +5.

// 858 / 590 / 268 as of decision 335: the seven flow actions plus the flow
// resource type. A flow is an ordered set of tool calls that one click sets
// off, so flow_run and flow_test_node are the two rows the platform most needs
// and were the two most conspicuously missing. Eight re-exports, same shape as
// every line below: vocabulary whose writers live elsewhere.

// 850 / 582 / 268 as of decision 334: edge_register and edge_delete, the two
// ends of a node's lifecycle (the middle — bytes, plugins, credential — was
// written by 332 and 333). Same shape as the lines below: vocabulary
// re-exports whose writers live in core/manager, outside this module.

// 848 / 580 / 268 as of decision 333: two supply-chain actions
// (edge_agent_upgrade, edge_package_upgrade), one webshell action
// (webshell_session_kill), their resource type (webshell_session), and the
// AddAuditEvent re-export in server/middleware. All five are re-exports and
// vocabulary constants whose writers live in core/manager, which is outside
// this module by design — same shape as every line below.
//
// 843 / 575 / 268 as of decision 332: the two edge actions (edge_rotate_secret,
// edge_plugin_set). Same shape as every line below it — a new action is a new
// constant in core/base/pkg/audit, and core/domains/model/audit must re-export
// every one of them or a caller in this module cannot see it (TestThePort-
// VocabularyIsFullyReExported is the gate, and it is the reason these two exist
// even though no core/domains code names them yet: their writers are manager
// HTTP handlers, which sit outside this module).
//
// This decision also showed what the ratchet is for. The first version added
// the aliases and nothing else, and the growth gate failed by itself. The
// tempting move was to drop the aliases and let the re-export gate fail
// instead — two gates, one decision, and the cheaper one to satisfy is the one
// that was wrong. **A ceiling you can satisfy by deleting the requirement is
// not a ceiling.** The number moved up by two, in the same commit as the two
// constants, with a sentence attached, which is the whole contract.
//
// 841 / 573 / 268 as of decision 324: the three Higress gateway actions
// (gateway_login, consumer_create, consumer_delete) and the
// gateway_consumer resource type.
//
// This is the first decision whose new constants are called from a package
// that is not reachable from the tree the walk measures AND live in a
// process that has its own chain: cmd/higress-console writes them into its
// own SQLite file (decision 324). The dead class is the same shape as every
// other audit constant here — declared in core/base/pkg/audit, called from a
// module this walk does not follow — but it is the first time the *reason*
// is a deployment boundary rather than a module boundary, which is worth
// writing down before somebody reads the number and assumes otherwise.
//
// 837 / 569 / 268 as of decision 323: the two hosted-page actions
// (page_delete, page_share) and the hosted_page resource type.
//
// Dead for a reason that is now stated in one place rather than three: these
// are declared in core/base/pkg/audit and called from cmd/opskeeper, and
// nothing inside the measured tree reaches across that line.
//
// 834 / 566 / 268 as of decision 322: the three panel actions (panel_create,
// panel_update, panel_delete) and the panel resource type. Four in, four
// dead — and the mechanism behind that is the one written below it, which
// this decision confirmed rather than discovered: the re-export in
// core/domains/model/audit keeps the copies reachable.
//
// 830 / 562 / 268 as of decision 320: the three chat-diagnose actions
// (chat_diagnose, chat_promote, chat_report) plus the chat_conversation
// resource type. Four symbols, four dead — exactly the count, and the
// test-only class did not move, which is what the measured triple now says
// rather than what a prediction says.
//
// Four constants produced exactly four, and the mechanism took a measurement
// to find rather than a reading to confirm. The four re-exports in
// core/domains/model/audit name the same four symbols, yet they are not
// counted again — not because the tool de-duplicates by name, but because
// `ActionChatDiagnose = auditport.ActionChatDiagnose` mentions the symbol
// inside its own package's non-test file, and a symbol mentioned that way is
// reachable by definition. The copies in log.go are precisely the reason the
// originals in port.go are still listed as dead: nothing inside the
// measured tree reaches core/base/pkg/audit from core/manager, but
// log.go does.
//
// This is the whole reason the number is measured every time instead of
// derived: a symbol count here depends on whether a re-export line happens
// to sit in a package's production files, and no amount of arithmetic on
// "constants added" gets there. Decisions 311 and 317 moved the total by
// more than their constant count for the same reason, in the opposite
// direction.
//
// 826 / 558 / 268 as of decision 319: the two self-healing-loop retry
// actions (recovery_retry_increment, recovery_retry_reset). Both are dead
// for the same reason decisions 311, 312, 314, 317 and 318 give — the
// declaration is in core/base/pkg/audit, the caller is in core/manager, and
// nothing inside the measured tree reaches across that line.
//
// The third number moved for a different reason, and it is the reason this
// decision measured instead of predicted. The budget said 270 and the tool
// said 268 *before this decision changed anything*, so the test-only class
// did not shrink; the ratchet had been carrying two of slack since it was
// set. Two things follow. A budget is not a measurement, and a class that
// drifts below its budget is invisible to a gate whose job is to trip on
// growth — which is why this comment states the measured triple rather than
// the movement.
//
// (The expected reading — two constants added, both dead, test-only
// unchanged — is what the tool then confirmed, so the prediction was right
// this time. That is luck, not method, and §4.251 already records the two
// times it was not.)
//
// 824 / 556 as of decision 318: the five node-agent conversation actions
// (agent_session_open, agent_message_send, agent_session_stop,
// agent_session_close, agent_decide) plus the agent_session resource type.
//
// All six are called from nodeagent/http.go, which this walk can see — and
// they still land in the *dead* class, not the reachable one. That is the
// same shape as decisions 311, 312, 314 and 317: the declarations live in
// core/base/pkg/audit, and the callers live in core/domains, so within the
// tree this walk measures there is nothing that reaches them. The first
// draft of this comment claimed the class split would hold because the
// constants are called; the count says otherwise, and the count is the
// thing the gate trips on.
//
// 818 / 550 as of decision 317, second pass: the same seven org /
// membership constants again, this time as the re-exports
// core/domains/model/audit is required to carry. The rule that caught it
// is TestTheReExportCoversTheWholeVocabulary, and it is worth noting what
// it means: a new action is not done when pkg/audit declares it, it is done
// when every module that re-exports the vocabulary has picked it up. The
// first bump in this decision was taken before that test had run.
//
// 811 as of decision 317: the seven new org / membership constants
// (org_create, org_update, org_delete, org_member_add, org_member_update,
// org_member_remove, and the org resource type) are all called from
// orgs.go, which this walk can see, so only the one helper the walk cannot
// reach — auditRefused, reached solely through a test — enters the count.
// The class split barely moves because the growth is reachable; the total is
// what moved, and it is the total the gate trips on.
//
// 810 / 543 as of decision 316: the four new ones are the credential vault's
// action constants (secret_create / secret_update / secret_delete) plus the
// secret resource type — the same re-export category decisions 311, 312 and
// 314 had to account for, read from core/domains through core/base/pkg/audit.
//
// 806 / 539 as of decision 314: the five new ones are the AgentTeams HITL
// proposal action/resource constants (hitl_proposal_{create,approve,reject,
// expire} + the hitl_proposal resource type). They are read only from the
// other module — core/manager/server/hitl reaches them through
// core/base/pkg/audit's re-exports — so this walk sees the originals as
// unreferenced from inside the core/base tree. Same category as the
// re-exported constants decisions 311 and 312 had to account for, raised in
// the same commit as the change that caused the growth.
const (
	unreachableBudget    = 876
	deadSymbolBudget     = 608
	testOnlySymbolBudget = 268
)

// TestTheUnreachableSymbolCountNeverGrows is the gate decision 199 declined to
// build, and it is built here for one narrow reason.
//
// Decision 199 measured that this walk cannot see six things: reflection,
// go:linkname, cgo //export, struct-tag codecs, embedded-method promotion and
// build tags. A symbol reachable only through one of those looks dead, so a
// gate on "is this symbol dead" would be a gate that cries wolf on a real but
// invisible path — and a gate that cries wolf teaches its reader to skip it.
// That reasoning is still correct and nothing here contradicts it.
//
// What the same reasoning does not cover is a gate on **growth**, because
// growth has to be justified by somebody either way. A symbol added with no
// production caller is a decision, and the decision was being made silently:
// the report grew, nobody read it, and three decisions later a documented
// control was still test-only while a boot log said it was loaded. That is
// the specific harm, it cost one ADR's worth of false confidence, and the
// cheapest thing standing between it and a repeat is making the growth
// require a sentence.
//
// The gate is deliberately one-sided. Lowering the numbers is not a failure —
// the budget is the ceiling, and a tree that deletes its way under it needs
// the pin moved down to keep meaning anything. So the constants above are
// edited in the same commit that brings the number down, and the failure
// message says so.
func TestTheUnreachableSymbolCountNeverGrows(t *testing.T) {
	// The root is located by a sibling this repository is known to have, not
	// by probing for go.work: the first version of this test used go.work as
	// its marker and scripts/modulecheck's TestTheRepositoryHasNoGoWorkProbes
	// rejected it, correctly — a file that asks whether a workspace is wired
	// up is a file that will not build in one. The marker here is
	// ../domaincheck, the same one the tree-smoke test above uses, so the two
	// tests in this package agree on where the repository is without either of
	// them caring how it is built.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "domaincheck")); err != nil {
		t.Skipf("not at the repository root: %v", err)
	}
	records, err := parseAll([]string{root})
	if err != nil {
		t.Fatalf("walking the shipped tree: %v", err)
	}
	res := analyse(records)

	if res.deadSymbols <= unreachableBudget &&
		res.deadOnlySymbols <= deadSymbolBudget &&
		res.testOnlySymbols <= testOnlySymbolBudget {
		return
	}
	t.Errorf("the tree now has %d unreachable symbols (%d dead, %d test-only), over the "+
		"ratchet of %d / %d / %d.\n"+
		"  Three ways out, and the first two are the point:\n"+
		"  1. the symbol is reachable and this walk cannot see it — say which of the six "+
		"invisible paths (reflection, go:linkname, cgo //export, struct-tag codecs, "+
		"embedded-method promotion, build tags) it uses, and add the fixture that proves it;\n"+
		"  2. the symbol is genuinely unreferenced — delete it, or say in "+
		"docs/opskeeper2-architecture.md why it is kept, with the same specificity the "+
		"decision log uses elsewhere;\n"+
		"  3. only then, if the growth is deliberate and recorded, raise the constants in "+
		"scripts/deadcode/ratchet_test.go in the same commit.\n"+
		"  Raising them silently is the one option that undoes this gate: the number only "+
		"means something while moving it costs a sentence.",
		res.deadSymbols, res.deadOnlySymbols, res.testOnlySymbols,
		unreachableBudget, deadSymbolBudget, testOnlySymbolBudget)
}
