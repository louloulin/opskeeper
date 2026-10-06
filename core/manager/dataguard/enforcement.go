package dataguard

// This file is the honest answer to a question the vocabulary kept answering
// wrong: what does Data-Guard actually enforce today?
//
// The sensitivity constants and the compliance tag both promise controls in
// their own doc comments — a reader role, an override, two approvers, an
// audit-retention period injected into the sandbox. Those promises were the
// kind that survive for years precisely because they live in comments and in
// a JSON column: nothing reads `enforced: true` and changes its behaviour, and
// nothing reports that it did not. So they are written down here, one row per
// promise, with the status each one actually has today.
//
// The status vocabulary is three words, not two, because "the code exists" and
// "the code runs" are different facts and only two words cannot tell them
// apart:
//
//   - StatusEnforced  the control runs on some path a request can take.
//   - StatusInert     the control is written and wired to nothing. Tests reach
//     it; production does not. This is the state that reads as "implemented"
//     from inside the package and as "absent" from outside it.
//   - StatusDeclared  there is no code at all. The promise lives in a comment
//     and in a persisted flag.
//
// Only StatusEnforced may be described as enforced anywhere else in the tree.
// Everything else is a declaration the console may show and nothing may rely
// on.

// Status is what a promised control actually does today.
type Status string

const (
	// StatusEnforced means the control runs on a path a request can take.
	StatusEnforced Status = "enforced"
	// StatusInert means the control is implemented and wired to nothing:
	// only tests reach it.
	StatusInert Status = "inert"
	// StatusDeclared means the promise has no implementation at all.
	StatusDeclared Status = "declared"
)

// ControlClaim is one promise, and what is true about it today.
//
// Probe is the symbol whose presence or absence in non-test code decides the
// row's status, and it is required for every row except a pure declaration —
// a registry that asserts reachability without naming what it is asserting
// reachability of is a registry of opinions.
type ControlClaim struct {
	// ID is stable and is what the gate and the ledger refer to.
	ID string
	// Claim is the promise, in the words the vocabulary uses.
	Claim string
	// Status is what is true today.
	Status Status
	// Probe is the symbol that proves the status: named for enforced and
	// inert rows, empty for declared ones.
	Probe string
	// Note says what stands in the control's place while it is not enforced,
	// or why nothing does.
	Note string
}

// Claims is the registry. It is a function rather than a package variable so
// a caller cannot mutate the shared slice and hand itself a cleaner report.
func Claims() []ControlClaim {
	return []ControlClaim{
		{
			ID: "compliance.enforced-tag",
			Claim: "a compliance tag with Enforced=true injects hard constraints into the cmdpolicy " +
				"sandbox: encryption-at-rest, audit-log-retention-1y, audit-log-retention-6mo, " +
				"audit-log-retention-6y, mfa-on-write, geo-eu-only, phi-encryption, " +
				"access-control-rbac, incident-response-24h, subject-erasure, purpose-limitation, " +
				"data-minimization, minimum-necessary, change-management, access-review-quarterly " +
				"and logical-access-logging",
			Status: StatusDeclared,
			Note: "nothing reads the flag: the sandbox has no compliance notion, and " +
				"retention and encryption controls have no implementation to inject. The flag " +
				"is a label the console may display and nothing may rely on.",
		},
		{
			ID:     "compliance.control-catalog",
			Claim:  "the five frameworks ship a recommended control list",
			Status: StatusEnforced,
			Probe:  "ControlCatalog",
			Note: "the row was declared because the catalog function was called by this registry " +
				"and nothing else: a one-click list had no route behind it. Decision 366 added " +
				"GET /v1/data-guard/compliance/frameworks, and the reason it is not simply the " +
				"directory served as JSON is that **an unqualified catalog is the same lie in " +
				"an API costume** — and an API costume is harder to argue with than a comment. " +
				"Of the sixteen names the five frameworks recommend, fourteen appear nowhere " +
				"but the catalog function and this registry, one appears only in a form " +
				"round-trip test, and one only on the public site. So every entry the endpoint " +
				"returns carries the status this build actually has, computed from this " +
				"registry rather than from a second list written beside it: a control that is " +
				"recommended here and enforced nowhere says so in the same response that " +
				"offers it. The catalog's status is `declared` for all sixteen, which is what " +
				"the compliance.enforced-tag row says and what the endpoint now says too.",
		},
		{
			ID:     "sensitivity.escalates-severity",
			Claim:  "TopSecret and Restricted raise a call to dangerous, Confidential to mutating",
			Status: StatusEnforced,
			Probe:  "ClassFor",
			Note: "the row was inert because PausePolicyImpl implements the mapping and nothing " +
				"constructs it in production. Decision 363 replaced that with a path that runs: " +
				"approval.Propose looks the action's target up in the label store before the row " +
				"exists and stores the raised class, so the signatures a reviewer is asked for " +
				"are decided by the label rather than by the producer's risk_class. Two failure " +
				"directions are closed deliberately — a lookup that errors refuses to create the " +
				"row rather than defaulting to unlabelled, and a label can only raise a class, " +
				"so labelling a database Internal is not a way to buy a single signature. A call " +
				"reaching several resources is judged by the strictest label in the set. See also " +
				"the read half, which has run since decision 361.",
		},
		{
			ID:     "sensitivity.confidential-reader-role",
			Claim:  "Confidential data requires the confidential-reader role",
			Status: StatusEnforced,
			Probe:  "AllowWithSensitivity",
			Note: "every console tool call now passes a reader-tier gate before the tool runs. " +
				"cmd/opskeeper assembles it from the label store and the iam enforcer, and " +
				"decorators.WithSensitivity wraps the tool bag on both the coordinator and the " +
				"worker path, so a caller whose tier does not reach the resource's label is " +
				"refused by name. The row was inert until decision 361, which was the first " +
				"thing in the tree to call the check that had been written for it.",
		},
		{
			ID:     "sensitivity.restricted-reader-role",
			Claim:  "Restricted data requires the restricted-reader role",
			Status: StatusEnforced,
			Probe:  "AllowWithSensitivity",
			Note: "the same gate as the confidential row, and it is a read gate: it asks the " +
				"enforcer about the read action, which is all the tier table can answer. A call " +
				"naming several resources is decided by the strictest label in the set, so the " +
				"order the caller wrote the list in does not decide the answer.",
		},
		{
			ID:    "sensitivity.restricted-write-override",
			Claim: "writing to a Restricted resource requires an override",
			Status: StatusDeclared,
			Note: "this half was split out of the restricted-reader row because the reader gate " +
				"that now runs does not implement it. The reader gate asks about the read " +
				"action, and an override is a grant that lets a write through anyway — which is " +
				"not what decision 363 added. That change raises an approval's class so the " +
				"write costs more signatures, a different control on a different side of the " +
				"queue, and it leaves the override this row names with no code behind it. A row " +
				"that said enforced because the escalation landed would be the exact lie this " +
				"registry exists to prevent, in the shape this registry was written to catch.",
		},
		{
			ID:     "sensitivity.top-secret-read-gated",
			Claim:  "TopSecret data is readable by nobody",
			Status: StatusEnforced,
			Probe:  "AllowWithSensitivity",
			Note: "**这一行改写了它自己的措辞，因为原措辞从来不是真的。** TopSecret 不是" +
				"「无人可读」，而是「持有 topsecret_reader tier 的人可读」——TierForSensitivity " +
				"一直这么定义，决策 361 把它接到了每一次控制台工具调用上。改写成可实现的那句之后 " +
				"它是真的：读 TopSecret 需要 tier，而 grant 这个 tier 是一条独立的、有记录的 " +
				"管理动作。**「无人可读」不是被实现了，是被撤回了**——一个能兑现的弱承诺比一个 " +
				"兑现不了的强承诺有用。",
		},
		{
			ID:     "approval.dual-sign",
			Claim:  "a destructive or cluster-scope approval needs two different administrators",
			Status: StatusEnforced,
			Probe:  "WithDualSignGate",
			Note: "决策 362 接上的。存储侧：approvals 行新增 signers_json，能放下 N 个签名人；" +
				"闸门侧：Sign 累积签名、缺口未补齐就保持 pending（HTTP 202），补齐才 Decide + 执行；" +
				"规则侧：policy/opskeeper/casbin/tenant_wide.json 从一份**匹配不到任何东西**的" +
				"配置（resource 写 tenant_wide 而没有一行审批是这么标的；role 写 opskeeper-admin " +
				"而系统里没有这个角色）改写成系统真实产出的词表。验证器本身也修了两个洞：按 " +
				"UserID 去重（此前同一个人签两次算数），以及真的去检查 rule.Role（此前该字段" +
				"从未被读过）。**仍有一处已知缺口**：生产者没有声明分类的行（目前是 mcp_call）" +
				"按单签放行，闸门不替它猜风险等级。",
		},
	}
}

// Claim returns the registry row with this id.
func Claim(id string) (ControlClaim, bool) {
	for _, claim := range Claims() {
		if claim.ID == id {
			return claim, true
		}
	}
	return ControlClaim{}, false
}

// DeclaredControls returns every control name the built-in frameworks mention
// without saying which of them this build enforces.
//
// The list is what an operator reading a label cannot tell apart, and it is
// exported so the gate that guards the registry is comparing against the same
// source the labels come from rather than a copy of it.
func DeclaredControls() []string {
	var out []string
	for _, framework := range AllFrameworks {
		out = append(out, DefaultFrameworkControls()[framework]...)
	}
	return out
}
