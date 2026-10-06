// Package hitl dual-sign validator for tenant_wide / blast_radius 高风险动作。
//
// ADR-019（docs/superpowers/decisions/2026-08-19-tenant-wide-dual-approval.md）：
//   - 高风险（blast_radius ∈ {cluster, tenant_wide} / data-guard destructive）
//     写操作需要双人审批，两签必须来自不同角色组（避免单点滥用）
//   - 角色与"必须组合"由 policy/opskeeper/casbin/tenant_wide.json 定义，
//     启动时载入；cmdpolicy 9 类策略 + Casbin RBAC + DualSignPolicy 三重门控
//
// 与 PausePolicyImpl.ShouldPause 的关系：
//   - ShouldPause 决定"要不要停"（输出 PauseReason.Metadata.dual_sign_required）
//   - DualSignPolicy.Validate 决定"签得够不够"（按角色组覆盖校验）
//
// **以上是设计。设计没有实现，而这一段曾经写成已实现。**
//
// 决策 285 量到的实况（`TestDualSignCannotBeEnforcedBecauseNowhereStoresTwoSigners`
// 是它的收据）：
//   - `Validate` 的调用方是 **0 个**。启动时 `cmd/opskeeper` 载入规则文件、
//     校验语法、打一行 "dual sign policy loaded"，然后那个局部变量出作用域。
//   - `Service.Approve` 第一次调用就把 `StatusApproved` 写下去并返回。
//   - `model.Approval` 只有 `ApprovedBy *uint64`，`model.Proposal` 只有
//     `ApprovedBy *uint64` 与 `ResumedBy *uint64`。**三列都是单值，没有一处
//     能放下第二个签名**——所以这不是「忘了接线」，是存储里没有接线要用的地方。
//   - `PausePolicyImpl.ShouldPause` 输出的 `dual_sign_required` 同样没有消费者：
//     闸门本身由 `pigagent.beforeToolCall` 判定，而它判的是 `read` 与「非 read」，
//     不读 severity。
//
// **因此「高风险动作需要两个不同角色组签核」这条 ADR-019 的核心结论，今天在
// 本仓里一次都没有生效过。** 唯一生效的审批栅栏是单签的：任何非只读的工具调用
// 都要一个人批准，批准时 `pigagent` 校验摘要绑定，**没有栅门时直接拒绝**
// （fail closed，见 `core/pig/pigagent/runstate.go`）。那段栅栏是真的，
// 本文件曾经描述的那段不是。
//
// 要让本文件描述的东西成真，需要三件事同时发生，缺一件就仍然是「配置了但没开」：
//  1. 存储：给 proposal 行一个能放下 N 个签名人的地方（迁移 + 模型列）；
//  2. 闸门：approve 路径上累积签名并调用 `Validate`，未签齐时保持 pending
//     而不是返回；
//  3. 声明：启动日志与本文件同时改成「已生效」。
//
// 第 3 件是前两件的收据，而**它今天已经在说谎**——决策 285 改掉了它，
// 在实现之前。
package hitl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Signer 表示一个审批人。
type Signer struct {
	// UserID 在审计日志里作为 approved_by 写入。
	UserID uint64

	// Role 是 Casbin role（"opskeeper-admin" / "opskeeper-observer" / 等）。
	// 来自 iam/biz/authz HydrateMemberships 同步。
	Role string

	// ApprovedAt 仅用于审计与限速；不影响校验逻辑。
	ApprovedAt int64 // unix seconds
}

// DualSignRule 一条 Casbin policy 规则。
//
// JSON 形态：
//
//	{"role":"opskeeper-admin","resource":"tenant_wide","action":"approve",
//	 "effect":"allow","requires":["opskeeper-admin","opskeeper-observer"]}
type DualSignRule struct {
	// Role 主签角色（policy.sub）；为空表示该 rule 不绑定主签角色，
	// 只看 resource+action+requires。
	Role string

	// Resource policy.obj。
	Resource string

	// Action policy.act。
	Action string

	// Effect "allow" / "deny"。
	Effect string

	// Requires 双签必须覆盖的角色组列表（每个元素至少出现一次）。
	// 空 → 单签即够；非空 → 任意两签只要覆盖所有 Requires 即合规。
	Requires []string
}

// DualSignPolicy 加载自 policy/opskeeper/casbin/tenant_wide.json 的内存形态。
type DualSignPolicy struct {
	mu    sync.RWMutex
	rules []DualSignRule
}

// NewDualSignPolicy 构造空 policy（用于单元测试 + 默认 fallback）。
func NewDualSignPolicy() *DualSignPolicy {
	return &DualSignPolicy{}
}

// LoadDualSignPolicies 从 JSON 文件载入策略。
//
// 文件不存在 → 返回空 policy + nil error（生产允许双签降级为单签，
// 需配合 cmdpolicy 风险等级共同判定）。
//
// 文件存在但解析失败 → 返回 error。
func LoadDualSignPolicies(path string) (*DualSignPolicy, error) {
	p := NewDualSignPolicy()
	if path == "" {
		return p, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return p, nil
		}
		return nil, fmt.Errorf("dual_sign: read %s: %w", path, err)
	}
	var doc struct {
		Policies []DualSignRule `json:"policies"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("dual_sign: parse %s: %w", path, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = doc.Policies
	return p, nil
}

// Add 运行时追加 rule（用于测试 + HotReload 预留）。
func (p *DualSignPolicy) Add(r DualSignRule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = append(p.rules, r)
}

// Rules 返回当前规则的快照（用于审计 / UI 渲染）。
func (p *DualSignPolicy) Rules() []DualSignRule {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]DualSignRule, len(p.rules))
	copy(out, p.rules)
	return out
}

// Validate 检查 signers 是否满足 resource+action 对应的双签要求。
//
// 返回 nil → 合规。
// 返回 DualSignError → 不合规，ErrKind 区分失败原因。
//
// 判定流程：
//  1. 找匹配 resource+action+effect=allow 的 rules
//  2. 取所有命中 rules 的 Requires 集合并集
//  3. 若 Requires 为空 → 单签即合规
//  4. 若 Requires 非空 → signers 必须 ≥ 2，且并集覆盖所有 Requires
//
// 角色组覆盖算法：
//   - 每个 signer.role 与 Requires 元素做精确匹配（大小写敏感）
//   - 同一 role 出现多次只算一组
//   - signers 顺序无关（验证后审计日志按 ApprovedAt 排序输出）
func (p *DualSignPolicy) Validate(resource, action string, signers []Signer) error {
	requires := p.collectRequires(resource, action)
	if len(requires) == 0 {
		// 无双签规则：单签即合规（调用方负责其它层校验）。
		if len(signers) == 0 {
			return &DualSignError{Kind: "missing_signer", Detail: "no signer on record"}
		}
		return nil
	}
	if len(signers) < 2 {
		return &DualSignError{
			Kind:   "insufficient_signers",
			Detail: fmt.Sprintf("requires %d signers, got %d", 2, len(signers)),
			Need:   requires,
		}
	}
	covered := map[string]struct{}{}
	for _, s := range signers {
		for _, req := range requires {
			if s.Role == req {
				covered[req] = struct{}{}
			}
		}
	}
	missing := []string{}
	for _, r := range requires {
		if _, ok := covered[r]; !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return &DualSignError{
			Kind:    "role_groups_uncovered",
			Detail:  fmt.Sprintf("signers do not cover required role groups: %v", missing),
			Need:    requires,
			Have:    signerRoles(signers),
			Missing: missing,
		}
	}
	return nil
}

// collectRequires 取所有匹配 resource+action 的 allow rules 的 Requires 并集。
func (p *DualSignPolicy) collectRequires(resource, action string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, r := range p.rules {
		if !strings.EqualFold(r.Effect, "allow") {
			continue
		}
		if !matchResource(r.Resource, resource) {
			continue
		}
		if !matchAction(r.Action, action) {
			continue
		}
		for _, req := range r.Requires {
			seen[req] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// matchResource "*" 通配；否则精确匹配。
func matchResource(rule, req string) bool {
	if rule == "*" || rule == "" {
		return true
	}
	return rule == req
}

// matchAction "*" 通配；否则大小写不敏感精确匹配。
func matchAction(rule, req string) bool {
	if rule == "*" || rule == "" {
		return true
	}
	return strings.EqualFold(rule, req)
}

func signerRoles(ss []Signer) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Role)
	}
	return out
}

// DualSignError 校验失败原因。
//
// ErrKind 取值：
//   - missing_signer: 没有任何签者
//   - insufficient_signers: 签者不足 2
//   - role_groups_uncovered: 签者角色未覆盖必需组
type DualSignError struct {
	Kind    string
	Detail  string
	Need    []string
	Have    []string
	Missing []string
}

func (e *DualSignError) Error() string {
	if e.Kind == "" {
		return "dual_sign: unknown error"
	}
	return fmt.Sprintf("dual_sign: %s: %s", e.Kind, e.Detail)
}
