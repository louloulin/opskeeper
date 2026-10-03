# opskeeper Makefile — 唯一构建/测试/部署入口（gospec 红线）
# 所有 CI / Dockerfile / README 都应只调 make target，禁裸 go build / docker build。

MODULE      := github.com/vincent-wuhan/opskeeper
PYTHON      ?= $(shell if [ -x .venv/bin/python ]; then echo .venv/bin/python; else echo python3; fi)
BIN_DIR     := bin
VERSION     := $(shell cat VERSION 2>/dev/null || git describe --tags --always --dirty 2>/dev/null || echo v0.0.0-dev)
LDFLAGS     := -X main.version=$(VERSION)
GO_BUILD    := go build -trimpath -ldflags '$(LDFLAGS)'

# Release/packaging paths
ifneq ($(filter command line environment,$(origin PLATFORM)),)
PLATFORM_PARTS := $(subst /, ,$(PLATFORM))
TARGET_OS   ?= $(word 1,$(PLATFORM_PARTS))
TARGET_ARCH ?= $(word 2,$(PLATFORM_PARTS))
else
TARGET_OS   ?= linux
TARGET_ARCH ?= amd64
PLATFORM    ?= $(TARGET_OS)/$(TARGET_ARCH)
endif
PACKAGE_TARGET := $(TARGET_OS)-$(TARGET_ARCH)
# Edge plugin / agent binaries ship amd64-only by default (edges are amd64 in
# our deployments) — independent of the manager's per-arch TARGET_ARCH. This is
# the big size lever: otelcol-contrib alone is ~290M per arch. Override to
# "linux-amd64 linux-arm64" to fetch/bundle more edge arches. Kept in sync with
# package.sh's EDGE_TARGETS (the staging side).
EDGE_PLUGIN_ARCHES ?= linux-amd64
STAGE       := dist/stage/opskeeper-$(VERSION)-$(PACKAGE_TARGET)
OUT         := dist/out
PACKAGE_CLEAN ?= 1

DB_DSN     ?= root:root@tcp(127.0.0.1:3306)/opskeeper?charset=utf8mb4&parseTime=true&loc=Local
MIGRATIONS := db/migrations
ONNXRUNTIME_VERSION ?= 1.20.1
ONNXRUNTIME_MIRROR ?= https://github.com/microsoft/onnxruntime/releases/download/v$(ONNXRUNTIME_VERSION)

.DEFAULT_GOAL := help

# ----------------------------------------------------------------------------
# help
# ----------------------------------------------------------------------------

.PHONY: help
help: ## 列出全部 target
	@awk 'BEGIN{FS=":.*##"; printf "Usage: make \033[36m<target>\033[0m\n\nTargets:\n"} \
	     /^[a-zA-Z0-9_\/-]+:.*##/ {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ----------------------------------------------------------------------------
# build
# ----------------------------------------------------------------------------

.PHONY: build build-opskeeper build-opskeeper-edge build-plugins test-plugins verify-plugins audit-open-source version-check
build: build-opskeeper build-opskeeper-edge ## 构建 opskeeper 与 opskeeper-edge

build-opskeeper: ## 构建云端 opskeeper
	@mkdir -p $(BIN_DIR)
	$(GO_BUILD) -o $(BIN_DIR)/opskeeper ./cmd/opskeeper

build-opskeeper-edge: ## 构建边端 opskeeper-edge
	@mkdir -p $(BIN_DIR)
	$(GO_BUILD) -o $(BIN_DIR)/opskeeper-edge ./cmd/opskeeper-edge

audit-open-source: ## 运行开源发布准入审计
	python3 scripts/audit_open_source.py

build-plugins: ## 构建 AgentTeams 插件发布包
	$(MAKE) -C plugins/agentteams-plugin-installer plugin-zip
	bash plugins/opskeeper-teamharness/scripts/build-package.sh

test-plugins: ## 运行插件测试
	bash -n scripts/demo_preflight.sh
	scripts/demo_preflight.sh --help >/dev/null
	npm test --prefix plugins/opskeeper-teamharness/dashboard
	$(MAKE) -C plugins/agentteams-plugin-installer self-check
	$(PYTHON) -m pytest tests/test_deterministic_archive.py tests/test_audit_open_source.py plugins/opskeeper-teamharness

# version-check is deliberately NOT a prerequisite of this target, and its
# absence is the reason the open-source gate below runs at all.
#
# check_release_version.py binds RELEASE_VERSION.json to one specific commit:
# it requires web_hash == `git rev-parse HEAD:web` and teamharness_source_tree
# == `git rev-parse HEAD:plugins/opskeeper-teamharness`, plus a rule that the
# release commit may only touch release metadata. Those assertions can only
# hold on the release commit itself, so on any 2.0 development commit the
# check is red by construction.
#
# It used to be a prerequisite here, and the arrangement was worse than having
# no gate: it sat in front of `audit_open_source.py`, so every push stopped at
# a release-time assertion and the open-source gate -- the one that catches a
# private path or a credential about to ship -- never executed. Meanwhile
# `make package`, which is what the release workflow actually runs, never
# depended on it either, so at release time it guarded nothing. It now runs
# where its assertions mean something, in .github/workflows/release.yml.
verify-plugins: build-plugins test-plugins ## 构建、测试并校验插件发布包
	$(PYTHON) scripts/verify_release.py
	python3 scripts/audit_open_source.py

version-check: ## 校验发布元数据与源码/插件版本一致（发布期门槛，见 .github/workflows/release.yml）
	python3 scripts/check_release_version.py

# ----------------------------------------------------------------------------
# test
# ----------------------------------------------------------------------------

.PHONY: test test-race test-integration test-e2e test-e2e-live e2e-delivery-check protocol-validate
test: ## 单元测试
	$(MAKE) protocol-validate
	go test ./...

protocol-validate: ## 校验 AgentTeams 跨语言协议与 golden examples
	ruby scripts/validate-agentteams-protocols.rb

test-race: ## 单元测试 + race
	go test -race ./...

test-integration: ## 集成测试（build tag: integration）
	go test -tags=integration ./...

test-e2e: ## E2E（默认 fakes，无外部凭证；catalog: docs/test/e2e-catalog.md）
	go test -tags=e2e -count=1 -timeout=30m ./tests/e2e/...

test-e2e-live: ## E2E live mode（用 tests/e2e/secrets.local.env 打通真实外部服务）
	E2E_LIVE_ALL=1 go test -tags=e2e -count=1 -timeout=15m ./tests/e2e/...

# 方案 0.4 的验收闸门：真二进制拓扑下的一次真实对话。
#
# 与 test-e2e 分开，是因为它要 Docker（frontier broker 容器）、要真构建
# pig 二进制，而这两件事都不是"跑一遍 e2e"该顺带做的。它也是本仓库里
# 唯一一个会同时起 manager 进程、edge 进程、pig 子进程和 broker 容器的
# 目标，因此也是唯一一个能发现"进程边界上形状不对"的闸门——在它之前，
# 有三个缺陷连续逃过了全部单元测试与进程内 e2e。
#
# DOCKER_HOST 不在这里设置。docker 客户端在没有 DOCKER_HOST 时用的就是
# `docker` 命令本身在用的那个 socket，而这个目标是全仓库唯一一个会起容器
# 的闸门——它默认指向 colima，就等于把绝大多数用 Docker Desktop 的人挡在
# 门外，而失败的样子是"连不上 docker"，看起来像测试坏了。需要 colima 的
# 人在自己的 shell 里设好 DOCKER_HOST，它会被原样带进来。
e2e-delivery-check: ## 节点 Agent 交付闭环（需 Docker；见 tests/e2e/README.md）
	go test -tags=e2e -count=1 -timeout=20m ./tests/e2e/ -run 'TestTheGatewayServesAStreamToANodeCredential|TestNodeAgentDelivery'

# The broker reaches operators two ways — built locally and shipped in the
# tarball, or pulled from Docker Hub — and upstream spells the two versions
# differently (git tag `v1.2.5`, published image `1.2.5`). Four files on the
# shipped side and two on the pulled side once disagreed, which meant the
# delivery acceptance was testing a broker the release never shipped. This
# is the check that keeps those six files saying the same thing.
.PHONY: broker-pin-check
broker-pin-check: ## 校验所有提到 frontier broker 版本的地方都指向同一个版本（决策 153）
	go run ./scripts/brokerpin .
	go test ./scripts/brokerpin/ -count=1

# The two corpus gates answer different prior questions, and both have to
# be asked. plugin-coverage asks whether a *plugin package* can serve an
# expectation; vocabulary asks whether the *system* can serve it at all.
# Neither subsumes the other, and today both report large gaps — which is
# only useful if the numbers are reproducible rather than remembered.
.PHONY: eval-gates eval-vocabulary eval-coverage eval-axes
eval-gates: eval-coverage eval-vocabulary eval-axes

# The joint verdict (a case is covered only when a package serves BOTH its
# root causes and its remediations) is 0/20 and always will be, because every
# shipped case names a remediation and no node package ships a write on
# purpose. A number that cannot move cannot catch a regression, so the gate
# is wired to the diagnosis axis instead: it moves when the fleet changes,
# and it is red today only for the four cases whose gaps are recorded with
# their reasons in core/floor/pluginmanifest.DiagnosisGaps.
eval-coverage: ## golden case 能力期望 vs 插件包能力（哪些 case 没有插件能服务）
	go run ./cmd/opskeeper-eval plugin-coverage --fail-on-unrecorded-diagnose-gap

eval-vocabulary: ## golden case 能力期望 vs 本构建真实词表（哪些 case 结构上无法满足）
	go run ./cmd/opskeeper-eval vocabulary

# judge 从本批起按 Localization × Identification × Reason 打分（2606.29193）。
# 一个 case 没声明的轴不产生数字，而"没测到"在读者眼里和 0 分没有区别——
# 所以语料里每个 case 都必须能测出三个轴。这条闸门盯的是这个，不是分数。
eval-axes: ## golden case 是否声明了三个诊断轴（能测才算数）
	go run ./cmd/opskeeper-eval axes --fail-on-unmeasured-axis

# Crystallization is the cost half of the plugin story: a fix that has been
# verified on its own several times does not need a model the next time, and
# the record that says so is the autonomy block of a package. The invariants
# that matter cannot be checked by reading a manifest after the fact — a
# promoted pattern has to be able to load, a retired one has to disappear, and
# a trial that is not evidence has to change nothing — so the gate runs them.
.PHONY: crystallize-check
crystallize-check: ## 结晶：晋升 / 退役 / 拒绝不可用输入 / 草稿能过真实校验器
	go test ./core/manager/biz/aiops/crystallize/ -count=1 -run \
		'TestTheEmittedDeclarationIsOneAPackageCanLoad|TestThreeCleanVerificationsPromoteAPattern|TestARollbackRetiresAPromotedPattern|TestADraftRefusesToOverwriteAPackage|TestAnUnusableTrialChangesNothing|TestTrialOfBuildsATrialTheLedgerAccepts'
	@echo "crystallize-check: promotion, retirement, refusal and load-through-admission are green"

# Marking foreign text as untrusted is a security claim, and a claim that
# nothing checks is a comment. The gate pins the four things the claim rests
# on: the marker an attacker would need to forge is drawn per render, a table
# (not a call site) says which tools are foreign, the shipped bag fences
# exactly that table, and the investigated prompt puts its three payloads
# inside blocks a payload cannot close.
.PHONY: promptguard-check
promptguard-check: ## 外来文本进模型前带 nonce 围栏（prompt injection 一条）
	go test ./core/manager/pkg/promptguard/ -count=1 -run \
		'TestABodyContainingTheClosingMarkerCannotCloseTheBlock|TestAMarkerWithAStaleIDCannotCloseThisBlock|TestEveryBlockGetsAFreshID|TestMarkerVariantsAreEscaped|TestParseRejectsWhatIsNotABlock|TestTheInstructionNamesTheTagTheFencerWrites|TestTheFenceCannotReachThePlatform'
	go test ./core/manager/biz/aiops/tools/decorators/ -count=1 -run \
		'TestTheResultIsFencedWithTheToolsOwnName|TestAnAdversarialResultCannotCloseTheFence|TestAnErrorIsNotFenced|TestInfoPassesThrough'
	go test ./core/manager/biz/aiops/tools/ -count=1 -run \
		'TestTheTableHasNoBlankOrDuplicateRows|TestLookupAgreesWithTheTable|TestMarkUntrustedOutputs|TestEveryNameInTheTableIsFencedInTheShippedBag|TestTheShippedBagFencesRatherThanJustWraps'
	go test ./core/manager/biz/loop/ -count=1 -run \
		'TestTheInvestigatedPromptMarksItsForeignBlocks|TestPayloadTextCannotCloseTheInvestigatedFence'
	@echo "promptguard-check: per-render markers, closed-list table, shipped bag and investigated prompt are green"

# "MCP compatible" is a claim about a *client*, and a claim only a client can
# test. The gate therefore drives pkg/mcpclient — the client this repository
# ships — against the real handler over a real HTTP round trip, and pins the
# four things the claim rests on: a stock client with no fleet header is
# accepted, the handshake answers the revision it asked for, ping is the empty
# utility the spec defines, and the tools a caller sees are the tools it may
# call (including the three whose seams are set last).
.PHONY: mcp-surface-check
mcp-surface-check: ## MCP 对外协议面：握手、保活、分页、可见性
	go test ./core/manager/server/mcp/ -count=1 -run \
		'TestOurOwnClientCanDriveOurOwnServer|TestPingIsTheEmptyReplyTheSpecDefines|TestInitializeEchoesTheRevisionTheClientAskedFor|TestInitializeStatesTheBoundary|TestEveryNotificationIsAcceptedWithoutABody|TestAStockMCPClientWithoutTheFleetVersionHeaderIsAccepted|TestAStatedForeignVersionIsStillRefused|TestToolsListPagesAndHandsBackACursor|TestAnUnparseableCursorIsAnErrorNotAPageOneRestart'
	go test ./core/manager/biz/aiops/tools/ -count=1 -run 'TestAToolWhoseSeamIsSetLaterIsAbsentUntilItIsSet'
	@echo "mcp-surface-check: handshake, keepalive, pagination, visibility and the late-seam trap are green"

# The audit port is a claim about a *boundary*, and the two places that
# boundary used to be written down — the exceptions ledger in
# scripts/modulecheck and the mayDependOn grant in .go-arch-lint.yml — are
# both artifacts a careless edit can quietly re-open. The gate therefore
# drives the tests that read those artifacts directly, not a grep: the
# import walk, the grant walk, the closed vocabulary, and the end-to-end
# path from a handler's SetAuditEvent to the row the writer persists.
#
# Since decision 110 the gate also covers the module as a whole: the port
# has to be the only way to *name* a row, so the writer itself is reachable
# from a table of declared holders, each with the reason it holds one. That
# table is the difference between a boundary and a convention — it is how
# the next domain that reaches for the writer finds out before review.
.PHONY: audit-port-check
audit-port-check: ## 审计端口：iam 不再反向依赖 manager，词表闭合，行照常落库
	go test ./core/manager/iam/server/ -count=1 -run \
		'TestThisContextReachesNothingAboveItself|TestEveryAuditRowThisContextEmitsIsNamedThroughThePort|TestTheArchitectureRulesGrantThisContextNothingAboveIt'
	go test ./core/manager/pkg/audit/ -count=1 -run \
		'TestTheSlotSurvivesEveryContextRewrap|TestOutsideAMiddlewareChainNothingIsRemembered|TestThePortCannotReachTheLedger|TestTheVocabularyIsWellFormed|TestOnlyTheThroatHoldsTheWriter|TestNoDomainOutsideTheListsReachesTheWriter'
	go test ./core/manager/model/audit/ -count=1 -run 'TestTheReExportCoversTheWholeVocabulary'
	go test ./core/manager/server/middleware/ -count=1 -run \
		'TestTheRowAHandlerAsksForIsTheRowTheLedgerGets|TestAnUnannotatedRequestIsNotAudited|TestAFailingRequestIsAuditedAsAFailure'
	@echo "audit-port-check: the port is BC-free, the vocabulary is closed, only the declared holders reach the writer, the grant is gone and rows still land"

# 决策 127：迁移必须在**生产的那个方言**上跑一次。
#
# 决策 126 之后试图把 manager 真正跑起来，boot 第二遍时死在一条迁移上：
#   DELETE FROM t WHERE id NOT IN (SELECT MIN(id) FROM t GROUP BY ...)
# 这句话 SQLite 接受，MySQL 直接报 1093。而这条迁移的测试**只有 SQLite**
# （core/manager/data/metric/store/migrate_test.go 用 glebarez/sqlite），
# 于是它带着一条绿测试发布，然后在第二次启动时炸——因为 dedupeRaw 在表还
# 不存在时会提前返回，第一次启动根本走不到那句。
#
# 所以闸门是「真 MySQL 上跑一遍」，而不是再加一条 SQLite 断言：
#   docker compose up -d mysql
#   OPSKEEPER_TEST_MYSQL_DSN='opskeeper:opskeeper@tcp(127.0.0.1:13306)/opskeeper_migtest?parseTime=true' \
#     make mysql-migration-check
# 变异验证（两条都做过）：
#   1. 把 dedupeTable 换回扁平子查询 → metric 包 3 条全红，SQLite 侧 14 条全绿。
#   2. 把 repair preview 的 CREATE INDEX 放回 schema 列表 → 清单级测试在
#      "boot #2" 上红，SQLite 侧 38 条全绿。
# 两次的共同点是 SQLite 侧始终是绿的：**当初漏出去的原因就在这里**，也是这条
# 闸门必须存在的理由。
.PHONY: mysql-migration-check
mysql-migration-check: ## 迁移在真 MySQL 上跑一遍（SQLite 抓不到方言差异）
	@test -n "$(OPSKEEPER_TEST_MYSQL_DSN)" || { \
		echo "mysql-migration-check: set OPSKEEPER_TEST_MYSQL_DSN to a scratch MySQL DSN"; \
		echo "  e.g. opskeeper:opskeeper@tcp(127.0.0.1:13306)/opskeeper_migtest?parseTime=true"; \
		exit 1; }
	go test -tags=integration ./core/manager/data/metric/store/ -count=1
	go test -tags=integration ./cmd/opskeeper/ -count=1 -run 'TestTheManagerSchemaReplays|TestThePassesActuallyBuiltASchema|TestEveryMigratorIsCalledOnEveryBoot'
	@echo "mysql-migration-check: the whole migration list runs three times on the dialect the deployment uses"

# ----------------------------------------------------------------------------
# lint
# ----------------------------------------------------------------------------

.PHONY: lint arch-lint arch-lint-run
lint: ## 运行 golangci-lint
	golangci-lint run

arch-lint: ## 运行 go-arch-lint（校验 BC 边界）
	@if command -v go-arch-lint >/dev/null 2>&1; then \
		go-arch-lint check; \
	else \
		echo "WARNING: go-arch-lint is not installed, so .go-arch-lint.yml is documentation only."; \
		echo "         The enforced subset (bounded contexts may not reach each other,"; \
		echo "         core/manager/pkg and core/floor stay business agnostic,"; \
		echo "         service goes through biz, and since decision 58 the"; \
		echo "         service -> biz <- data direction) runs"; \
		echo "         under 'make module-check'. Install go-arch-lint, or run"; \
		echo "         'make arch-lint-run' to fetch and run it without installing."; \
	fi

arch-lint-run: ## 不安装、直接用 go run 跑 go-arch-lint（首次需要网络）
	go run github.com/fe3dback/go-arch-lint@latest check

module-check: ## 校验 OpsKeeper 2.0 模块边界（唯一 PiG 导入点 / core 无基础设施依赖）
	go run ./scripts/modulecheck .

# The release chain already puts the right binary in the right directory --
# build-edge-bundle.sh derives its source dir from the arch argument it is
# handed -- and nothing anywhere checks the artefact inside it. Four
# cross-compile targets from one Makefile is four chances to write a host
# build into a cross slot, and that failure only appears on a customer node
# as ENOEXEC: no log line, no health check, just a tool call that never
# returns. `go version -m` reads the GOOS/GOARCH/CGO_ENABLED the compiler
# recorded in the binary itself, which is the only account that can
# contradict the filename.
#
# The agent is required for all four targets; the edge is checked wherever it
# happens to be built, so this gate is useful after build-pig-all alone. When
# no edge is present the report says the pair rule did not run rather than
# letting a green line stand in for coverage that was never exercised.
.PHONY: node-arch-check
node-arch-check: ## 校验 bin/<os>-<arch>/ 里节点的 pig 与 edge 真的是该架构（决策 134）
	go run ./scripts/nodearch .
	go test ./scripts/nodearch/ -count=1

# modulecheck stops at the module and go-arch-lint stops at the layer, and
# inside core/manager neither can see a domain: the arch-lint components are
# named after layers (manager_biz, manager_model, ...), so biz/alert
# importing biz/loop is manager_biz -> manager_biz and every rule allows it.
# Seven pairs of domains in the tree already reach each other both ways.
# This gate makes those edges declared, and a new one red.
.PHONY: domain-check
domain-check: ## 校验 control plane 的域边界（55 个域 / 42 条声明边 / 0 对环，决策 118 起）
	go run ./scripts/domaincheck .
	go test ./scripts/domaincheck/ -count=1

# The plan's section 6 names its acceptance gates in a sentence, and two of
# the three were green only on the machine of whoever typed them: `eval-gates`
# and `domain-check` ran nowhere automatic. A gate nothing executes is a gate
# that does not exist, and this repository has watched seven declared cycles
# come back twice. This target reads the Makefile and ci.yml and fails when a
# promised gate is missing from either -- it does not re-run the gates, since
# CI runs them three lines above and the answer is on the same page.
.PHONY: ci-gate-check
ci-gate-check: ## 校验计划 §六 的验收门槛都已定义并真的被 CI 调用（决策 163）
	go run ./scripts/cigate .
	go test ./scripts/cigate/ -count=1

# Report only, never a gate: a name-based reachability walk cannot see
# interface satisfaction, reflection, cgo or go:linkname, so a red build on
# its output would train people to add "trust me" comments. The number it
# prints is the size of the wire-it-up-or-delete-it backlog, which is what
# stage 3 needs before choosing between cutting volume and splitting it.
deadcode-report: ## 报出生产代码里只有测试引用的符号（报告，不闸门）
	go run ./scripts/deadcode . core core/edge core/pig core/manager core/floor core/harness sdk
	go test ./scripts/deadcode/ -count=1

# A split proposal written on the day it is wrong is a proposal nobody
# argues with, because the tool that says it is wrong also breaks the
# build. So these two are reports: the gate above keeps its verdict, and
# these only print what a grouping would cost.
.PHONY: domain-graph
domain-graph: ## 打印 control plane 域图（入出度排行 / 最长路径分层 / 纠缠对，不闸门）
	go run ./scripts/domaincheck . -graph

.PHONY: split-cost
split-cost: ## 给一份分组方案定价：跨组 import 语句数 + 被切断的边（不闸门）
	@test -n "$(FILE)" || { echo 'usage: make split-cost FILE=docs/manager-split.proposed'; exit 2; }
	go run ./scripts/domaincheck . -cut $(FILE)

# The other half of what split-cost cannot say. That number prices a cut by
# import edges, which say two packages must be BUILT together and say nothing
# about whether anyone ever CHANGES them together. The proposal names three
# missing facts and this reports on the one the history can answer.
.PHONY: domain-cochange
domain-cochange: ## 打印各域的独立改动率、它背后的段数与诞生段占比、共变对（不闸门；证据，不是裁决）
	go run ./scripts/cochange/
	go test ./scripts/cochange/ -count=1

# The root `make test` no longer reaches core/harness: it is a separate Go
# module now, and that separation is the point. Anything that wants the
# whole repository tested has to say so explicitly, or the golden-case
# corpus silently stops being run.
module-test: ## 运行新模块（core / pig / edge / floor / manager / harness / sdk）的测试
	cd core && go test ./... -count=1
	cd core/pig && go test ./... -count=1
	cd core/edge && go test ./... -count=1
	cd core/floor && go test ./... -count=1
	cd core/manager && go test ./... -count=1
	cd core/harness && go test ./... -count=1
	cd sdk && go test ./... -count=1
	cd core && go build ./...
	cd core/pig && go build ./...
	cd core/edge && go build ./...
	cd core/floor && go build ./...
	cd core/manager && go build ./...
	cd core/harness && go build ./...
	cd sdk && go build ./...

module-race: ## 对新模块跑竞态检测（supervisor 重启循环是并发热点）
	cd core && go test ./... -count=1 -race
	cd core/pig && go test ./... -count=1 -race
	cd core/edge && go test ./... -count=1 -race
	cd core/floor && go test ./... -count=1 -race
	cd core/manager && go test ./... -count=1 -race
	cd core/harness && go test ./... -count=1 -race

# ----------------------------------------------------------------------------
# pig pin
# ----------------------------------------------------------------------------
#
# The published PiG dependency is a tag in six go.mod files, and this is the
# only place in the repository that can turn that tag back into a directory on
# somebody's disk. It matters because the two states are not the same build:
# a workspace build resolves siblings through go.work, a release build
# resolves them through the replace directives in each go.mod, and only one of
# those is what a node running `go build` on a plugin will reproduce.
#
#   module-standalone-check
#                  the gate. Builds and tests every module with GOWORK=off, so
#                  what is proven is what CI and a node will see: the
#                  published tags and the replace directives in each go.mod,
#                  with no workspace. Prefix it with GOPROXY=off to also prove
#                  the module cache is complete, which is what a sealed build
#                  host looks like; CI needs the proxy, so it does not.
#   pig-dev-pin    opt in to a local checkout, for the days someone is
#                  changing PiG itself. It edits go.work, which is gitignored,
#                  so the override cannot be committed by accident.
#   pig-dev-unpin  undo it. Run this before trusting a green test again.
#
# This target is not a duplicate of module-test. go.work is gitignored, so a
# workspace build resolves the sibling modules and the PiG tag through a file
# CI never has; the two builds disagree exactly when a go.mod is wrong, which
# is invisible until a release tries to build without the file. Two real
# defects lived in that gap: a go.sum without the yaml.v3 go.mod hash, and a
# root go.mod whose core/floor requirement only ever worked because go.work
# covered it.
#
# A green `make module-test` is a statement about the workspace. This one is a
# statement about what ships.

PIG_MODULES := . core core/edge core/floor core/harness core/manager core/pig \
	core/pig/extensions/opskeeper-gate \
	core/pig/extensions/opskeeper-sre-readonly \
	core/pig/extensions/opskeeper-sre-middleware \
	core/pig/extensions/opskeeper-sre-observability \
	core/pig/extensions/opskeeper-sre-repair \
	core/pig/extensions/opskeeper-sre-autonomy \
	sdk

# Deliberately not defaulted to a path on any one developer's machine. A
# checked-in default is how a replace directive comes back by accident.
PIG_DEV_PATH ?=

.PHONY: module-standalone-check pig-dev-pin pig-dev-unpin plugin-extension-build-check pig-tool-scoping-check
module-standalone-check: ## 关掉 workspace 与代理，按发布条件构建并测试全部模块
	@for m in $(PIG_MODULES); do \
		echo "  standalone: $$m"; \
		( cd $$m && GOWORK=off go build ./... && GOWORK=off go test ./... -count=1 ) || exit 1; \
	done
	@echo "standalone: every module builds and tests on its own, on the published tags"

# The node builds every packaged plugin extension from source, with the
# workspace off, on a machine that has never heard of this repository. That
# is a different build from every other one in this Makefile, so it gets its
# own target: nothing else here would notice if the packages stopped
# resolving on a node while continuing to build perfectly in-tree.
#
# The gate lives in the test suite (TestEveryPackagedExtensionBuildsTheWayThe-
# NodeBuildsIt, so it cannot be forgotten) and runs as part of
# module-standalone-check. This target is the fast, named way to run just
# that gate while working on a package.
plugin-extension-build-check: ## 按节点的方式构建每个打包扩展（GOWORK=off，节点无本地 checkout）
	@go test ./core/floor/pluginmanifest/ -count=1 -run 'TestEveryPackaged'
	@echo "plugin-extension-build-check: every packaged extension builds the way a node builds it"

pig-dev-pin: ## 本地改 PiG 时用：make pig-dev-pin PIG_DEV_PATH=/path/to/PiG
	@test -n "$(PIG_DEV_PATH)" || { echo "usage: make pig-dev-pin PIG_DEV_PATH=/path/to/PiG"; exit 1; }
	@test -d "$(PIG_DEV_PATH)" || { echo "no such directory: $(PIG_DEV_PATH)"; exit 1; }
	go work edit -replace github.com/MichaelKinsy/PiG=$(PIG_DEV_PATH)
	@echo "pig-dev-pin: the workspace now builds against $(PIG_DEV_PATH)."
	@echo "             Tests here prove nothing about the tag. Run 'make pig-dev-unpin' then 'make module-standalone-check'."

pig-dev-unpin: ## 撤销本地 PiG checkout 覆盖，回到固定 tag
	go work edit -dropreplace github.com/MichaelKinsy/PiG
	@echo "pig-dev-unpin: back on the published tag. Verify with 'make module-standalone-check'."

# 节点 Agent 到底被提供了哪些工具——用真二进制回答。
#
# 单元测试回答不了这个问题：piglet.ScopeTools 是这条链路上唯一可被本仓库
# 直接调用的部分，而真实运行时的工具来源分类发生在 PiG 内部一个未导出的
# 转换里，单元测试只能用**手搓**的来源去喂它。于是它证明了「若运行时这样
# 分类则 profile 正确」，而运行时并不这样分类（§4.67）。
#
# 这个目标今天会红，红的原因在上游 PiG，不在本仓库。它不进 `make test`：
# 一个长期红的测试只会训练所有人忽略红色。它也不该被删掉——它是这个缺陷
# 唯一的可执行证据，上游修好后它会自己转绿。
#
# 它默认测的是**固定 tag**：pigBinary() 用 GOWORK=off 构建，于是 go.work 里
# 的本地 replace 被绕过（这是刻意的，见 runtime_scoping_test.go 的注释）。
# 因此 `make pig-dev-pin` 对本目标无效。今天唯一能让它转绿的方式是把带修复
# 的二进制喂进来（§4.93.6）：
#
#     GOWORK=off go build -o /tmp/pig ./cmd/pig    # 在带修复的 PiG 目录里
#     OPSKEEPER_PIG_BIN=/tmp/pig make pig-tool-scoping-check
#
# 上游发布含修复的 tag 之前，这条路是它唯一的绿灯来源。实测 18/18。
.PHONY: pig-tool-scoping-check
pig-tool-scoping-check: ## 真二进制验证节点 Agent 被提供了插件工具（默认红；OPSKEEPER_PIG_BIN=本地构建可转绿，见上）
	go test -tags pigscoping -count=1 -timeout 10m ./core/pig/pigprofile/ \
		-run TestTheNodeProfileActuallyOffersTheToolsItsPackagesDeclare

# ----------------------------------------------------------------------------
# proto
# ----------------------------------------------------------------------------

.PHONY: proto
proto: ## [api] 重新生成 proto（优先 buf，回退 protoc + protoc-gen-go/grpc）
	@if command -v buf >/dev/null 2>&1; then \
		echo "buf generate"; \
		cd api && buf generate; \
	else \
		echo "buf not installed; falling back to protoc"; \
		command -v protoc >/dev/null 2>&1 || { echo "protoc also missing"; exit 1; }; \
		command -v protoc-gen-go >/dev/null 2>&1 || { echo "protoc-gen-go missing (go install google.golang.org/protobuf/cmd/protoc-gen-go@latest)"; exit 1; }; \
		command -v protoc-gen-go-grpc >/dev/null 2>&1 || { echo "protoc-gen-go-grpc missing (go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest)"; exit 1; }; \
		mkdir -p api/gen; \
		cd api && protoc --proto_path=. \
			--go_out=gen --go_opt=paths=source_relative \
			--go-grpc_out=gen --go-grpc_opt=paths=source_relative \
			--go-grpc_opt=require_unimplemented_servers=true \
			frontierbound/v1/frontierbound.proto; \
	fi

# ----------------------------------------------------------------------------
# migrate
# ----------------------------------------------------------------------------

.PHONY: migrate-up migrate-down
migrate-up: ## DB migrate up（DB_DSN 可覆盖）
	migrate -path $(MIGRATIONS) -database "mysql://$(DB_DSN)" up

migrate-down: ## DB migrate down 1 步
	migrate -path $(MIGRATIONS) -database "mysql://$(DB_DSN)" down 1

# ----------------------------------------------------------------------------
# docker
# ----------------------------------------------------------------------------

.PHONY: docker docker-opskeeper docker-opskeeper-edge
docker: docker-opskeeper docker-opskeeper-edge ## 构建全部镜像

docker-opskeeper: ## 构建 opskeeper 镜像
	docker build --build-arg VERSION=$(VERSION) -t opskeeper:$(VERSION) -f deploy/Dockerfile.opskeeper .

docker-opskeeper-edge: ## 构建 opskeeper-edge 镜像
	docker build -t opskeeper-edge:$(VERSION) -f deploy/Dockerfile.opskeeper-edge .

# ----------------------------------------------------------------------------
# compose
# ----------------------------------------------------------------------------

.PHONY: compose-up compose-down
compose-up: ## 本地 docker compose 启动
	docker compose -f deploy/docker-compose.yml up -d

compose-down: ## 本地 docker compose 停止
	docker compose -f deploy/docker-compose.yml down

# ----------------------------------------------------------------------------
# run
# ----------------------------------------------------------------------------

.PHONY: run-opskeeper run-opskeeper-edge
run-opskeeper: ## 本地直接跑 opskeeper
	go run ./cmd/opskeeper

run-opskeeper-edge: ## 本地直接跑 opskeeper-edge
	go run ./cmd/opskeeper-edge

# ----------------------------------------------------------------------------
# Release / packaging
# ----------------------------------------------------------------------------
# Produces a single, self-contained tarball ready to scp to any Linux box with
# docker + docker compose installed:
#
#     dist/out/opskeeper-$(VERSION)-linux-amd64.tar.xz
#     dist/out/opskeeper-$(VERSION)-linux-arm64.tar.xz  (make package TARGET_ARCH=arm64)
#
# Pipeline (wired via `make package`):
#   1. build-edge-all   — cross-compile opskeeper-edge for 4 targets.
#   2. docker-build     — docker build opskeeper:$(VERSION) for $(PLATFORM).
#   3. dist/package.sh  — stage + docker save + tar.xz + sha256.

.PHONY: build-linux
build-linux: ## [release] 交叉编译 opskeeper linux/amd64
	@mkdir -p $(BIN_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/linux-amd64/opskeeper ./cmd/opskeeper
	@echo "built $(BIN_DIR)/linux-amd64/opskeeper"

# ---- node AI agent (pig) ---------------------------------------------------
# The node's AI agent is `pig`, and it ships inside the edge the same way the
# other bundled binaries do. That is not a packaging preference: the edge
# spawns it as a child process, and a node whose agent is missing is a node
# that starts, authenticates, answers "how are you" and has no tools at all.
# The exporters are optional because a node without them loses one signal;
# this one is not optional, which is why the bundle treats it differently.
#
# The build runs from core/pig, not from the repo root, and that is the whole
# point of the target. core/pig is the only module that requires PiG, and it
# requires the *published tag* — the same condition `make module-standalone-check`
# verifies. Building from the repo root would honour go.work, so a developer
# with PiG replaced by a local checkout would ship a node running an agent built
# from code that was never tagged, reviewed, or released. GOWORK=off makes that
# impossible to do by accident.
PIG_CMD := github.com/MichaelKinsy/PiG/cmd/pig
PIG_LDFLAGS := -s -w

.PHONY: build-pig-all
build-pig-all: build-pig-linux-amd64 build-pig-linux-arm64 build-pig-darwin-amd64 build-pig-darwin-arm64 ## [release] 交叉编译节点 AI Agent (pig) 全部 4 个目标
	@echo "built all node agent binaries in $(BIN_DIR)/<os>-<arch>/pig"

.PHONY: build-pig-linux-amd64
build-pig-linux-amd64: ## [release] 节点 AI Agent linux/amd64
	@mkdir -p $(BIN_DIR)/linux-amd64
	cd core/pig && GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "$(PIG_LDFLAGS)" \
		-o $(CURDIR)/$(BIN_DIR)/linux-amd64/pig $(PIG_CMD)

.PHONY: build-pig-linux-arm64
build-pig-linux-arm64: ## [release] 节点 AI Agent linux/arm64
	@mkdir -p $(BIN_DIR)/linux-arm64
	cd core/pig && GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "$(PIG_LDFLAGS)" \
		-o $(CURDIR)/$(BIN_DIR)/linux-arm64/pig $(PIG_CMD)

.PHONY: build-pig-darwin-amd64
build-pig-darwin-amd64: ## [release] 节点 AI Agent darwin/amd64
	@mkdir -p $(BIN_DIR)/darwin-amd64
	cd core/pig && GOWORK=off GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "$(PIG_LDFLAGS)" \
		-o $(CURDIR)/$(BIN_DIR)/darwin-amd64/pig $(PIG_CMD)

.PHONY: build-pig-darwin-arm64
build-pig-darwin-arm64: ## [release] 节点 AI Agent darwin/arm64
	@mkdir -p $(BIN_DIR)/darwin-arm64
	cd core/pig && GOWORK=off GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "$(PIG_LDFLAGS)" \
		-o $(CURDIR)/$(BIN_DIR)/darwin-arm64/pig $(PIG_CMD)

.PHONY: build-edge-all
build-edge-all: build-edge-linux-amd64 build-edge-linux-arm64 build-edge-darwin-amd64 build-edge-darwin-arm64 ## [release] 交叉编译 opskeeper-edge + 节点 AI Agent 全部 4 个目标
	@echo "built all edge binaries in $(BIN_DIR)/<os>-<arch>/{opskeeper-edge,pig}"

.PHONY: build-edge-linux-amd64
build-edge-linux-amd64: build-pig-linux-amd64 ## [release] edge linux/amd64（含同架构节点 AI Agent）
	@mkdir -p $(BIN_DIR)/linux-amd64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/linux-amd64/opskeeper-edge ./cmd/opskeeper-edge

.PHONY: build-edge-linux-arm64
build-edge-linux-arm64: build-pig-linux-arm64 ## [release] edge linux/arm64（含同架构节点 AI Agent）
	@mkdir -p $(BIN_DIR)/linux-arm64
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/linux-arm64/opskeeper-edge ./cmd/opskeeper-edge

.PHONY: build-edge-darwin-amd64
build-edge-darwin-amd64: build-pig-darwin-amd64 ## [release] edge darwin/amd64（含同架构节点 AI Agent）
	@mkdir -p $(BIN_DIR)/darwin-amd64
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/darwin-amd64/opskeeper-edge ./cmd/opskeeper-edge

.PHONY: build-edge-darwin-arm64
build-edge-darwin-arm64: build-pig-darwin-arm64 ## [release] edge darwin/arm64（含同架构节点 AI Agent）
	@mkdir -p $(BIN_DIR)/darwin-arm64
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BIN_DIR)/darwin-arm64/opskeeper-edge ./cmd/opskeeper-edge

.PHONY: fetch-onnxruntime docker-build
fetch-onnxruntime: ## [release] 按 TARGET_ARCH 准备并校验 ONNX Runtime 离线缓存
	bash scripts/fetch_onnxruntime.sh "$(ONNXRUNTIME_VERSION)" "$(TARGET_ARCH)" "$(ONNXRUNTIME_MIRROR)"

docker-build: fetch-onnxruntime ## [release] 构建 opskeeper:$(VERSION) 镜像（默认 linux/amd64，可用 PLATFORM 覆盖）
	docker buildx build \
		--platform $(PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg ONNXRUNTIME_VERSION=$(ONNXRUNTIME_VERSION) \
		--build-arg ONNXRUNTIME_MIRROR=$(ONNXRUNTIME_MIRROR) \
		-t opskeeper:$(VERSION) \
		-f deploy/Dockerfile.opskeeper \
		$(DOCKER_BUILD_CACHE_ARGS) \
		--load .

# Frontend SPA + nginx (ADR-008). The image bakes web/dist/ into nginx so it
# can serve standalone; nginx.conf and TLS certs are bind-mounted at runtime.
.PHONY: build-web
build-web: ## [release] 编译前端 SPA 到 web/dist/
	cd web && pnpm install --frozen-lockfile && pnpm run build

.PHONY: docker-build-web
docker-build-web: ## [release] 构建 opskeeper-web:$(VERSION) 镜像（前端 SPA + nginx）
	docker buildx build \
		--platform $(PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		-t opskeeper-web:$(VERSION) \
		-f deploy/Dockerfile.web \
		$(DOCKER_BUILD_WEB_CACHE_ARGS) \
		--load .

# Frontier broker is upstream singchia/frontier (ADR-007). Docker Hub pull
# is unreliable in some networks, so we build the image locally from the
# upstream source and ship it in the release tarball.
FRONTIER_SRC     ?= $(HOME)/frontier
FRONTIER_VERSION ?= v1.2.5
FRONTIER_BUILD_FORCE ?= 1

.PHONY: docker-build-broker
docker-build-broker: ## [release] 本地构建 singchia/frontier:$(FRONTIER_VERSION)
	@existing_platform=$$(docker image inspect -f '{{.Os}}/{{.Architecture}}' singchia/frontier:$(FRONTIER_VERSION) 2>/dev/null || true); \
	if [ "$(FRONTIER_BUILD_FORCE)" != "1" ] && [ "$$existing_platform" = "$(PLATFORM)" ]; then \
		echo "[broker] singchia/frontier:$(FRONTIER_VERSION) already present for $(PLATFORM) — skipping rebuild"; \
	else \
		test -d $(FRONTIER_SRC) || { echo "FRONTIER_SRC=$(FRONTIER_SRC) not found and local image is not for $(PLATFORM)"; exit 1; }; \
		docker buildx build \
			--platform $(PLATFORM) \
			-t singchia/frontier:$(FRONTIER_VERSION) \
			-f deploy/Dockerfile.frontier \
			$(DOCKER_BUILD_BROKER_CACHE_ARGS) \
			--load $(FRONTIER_SRC); \
	fi

.PHONY: docker-save
docker-save: ## [release] docker save opskeeper:$(VERSION) 到 stage
	@mkdir -p $(STAGE)/images
	docker save opskeeper:$(VERSION) -o $(STAGE)/images/opskeeper.tar
	@echo "saved $(STAGE)/images/opskeeper.tar"

# Promtail bundle (ADR-012 / ADR-015 logs plugin).
# Cached under bin/<os>-<arch>/promtail to avoid re-downloading on every build.
PROMTAIL_VERSION ?= 3.4.0
FETCH_CURL_FLAGS ?= -fL --retry 3 --retry-all-errors --retry-delay 3 --connect-timeout 15 --speed-time 60 --speed-limit 1024 --show-error

.PHONY: fetch-promtail
fetch-promtail: ## [release] 下载 promtail 到 bin/<os>-<arch>/promtail (Grafana 只发 linux 版本)
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/promtail; \
		if [ -f $$dest ]; then \
			echo "[promtail] $$dest already present — skip"; \
			continue; \
		fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		zip=/tmp/promtail-$$os-$$arch.zip; \
		url=https://github.com/grafana/loki/releases/download/v$(PROMTAIL_VERSION)/promtail-$$os-$$arch.zip; \
		echo "[promtail] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$zip $$url || { echo "promtail download failed for $$target"; exit 1; }; \
		unzip -p $$zip > $$dest; \
		chmod +x $$dest; \
		rm -f $$zip; \
		echo "[promtail] staged $$dest"; \
	done
	@echo "[promtail] note: Grafana doesn't ship darwin binaries — edge on macOS hosts will see logs plugin disabled (warned by install-edge.sh)"

# OpenTelemetry Collector contrib bundle (ADR-013 / ADR-015 traces plugin).
# Cached under bin/<os>-<arch>/otelcol-contrib. Note: contrib build is
# ~200MB uncompressed per platform — operators wanting a slimmer agent can
# swap in a custom OCB build (otel-collector-builder); we ship contrib so
# default install works without forcing users to compile their own.
OTELCOL_VERSION ?= 0.118.0

.PHONY: fetch-otelcol
fetch-otelcol: ## [release] 下载 otelcol-contrib 到 bin/<os>-<arch>/otelcol-contrib (linux-only)
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/otelcol-contrib; \
		if [ -f $$dest ]; then \
			echo "[otelcol] $$dest already present — skip"; \
			continue; \
		fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/otelcol-contrib-$$os-$$arch.tar.gz; \
		url=https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v$(OTELCOL_VERSION)/otelcol-contrib_$(OTELCOL_VERSION)_$${os}_$${arch}.tar.gz; \
		echo "[otelcol] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { echo "otelcol-contrib download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $(BIN_DIR)/$$target otelcol-contrib || { echo "extract failed for $$target"; exit 1; }; \
		chmod +x $$dest; \
		rm -f $$tgz; \
		echo "[otelcol] staged $$dest"; \
	done
	@echo "[otelcol] note: contrib distro is ~200MB per platform; operators wanting smaller agent can build a custom OCB collector and drop it under /usr/local/lib/opskeeper-edge/otelcol-contrib"

# node_exporter — host metric source bundled with the edge package
# (CPU / memory / disk / network / load). Without this, install-edge
# leaves the operator without a metric source on the host and Monitor
# panels stay empty. Cached under bin/<os>-<arch>/node_exporter.
NODE_EXPORTER_VERSION ?= 1.8.2

# process-exporter — per-process metrics (groupable by comm / cmdline)
# used to back the "Top N processes timeline" panel via PromQL
# instead of the on-demand gopsutil RPC. Cached under
# bin/<os>-<arch>/process_exporter. Sticks with the Prometheus
# ecosystem (matches node_exporter's deploy + metric-naming model)
# rather than mixing in otelcol hostmetrics.
PROCESS_EXPORTER_VERSION ?= 0.8.4
MYSQLD_EXPORTER_VERSION ?= 0.19.0
POSTGRES_EXPORTER_VERSION ?= 0.19.1
REDIS_EXPORTER_VERSION ?= 1.86.0
MONGODB_EXPORTER_VERSION ?= 0.51.0

.PHONY: fetch-node-exporter
fetch-node-exporter: ## [release] 下载 node_exporter 到 bin/<os>-<arch>/node_exporter (linux-only)
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/node_exporter; \
		if [ -f $$dest ]; then \
			echo "[node_exporter] $$dest already present — skip"; \
			continue; \
		fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/node_exporter-$$os-$$arch.tar.gz; \
		url=https://github.com/prometheus/node_exporter/releases/download/v$(NODE_EXPORTER_VERSION)/node_exporter-$(NODE_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[node_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { echo "node_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz --strip-components=1 -C $(BIN_DIR)/$$target node_exporter-$(NODE_EXPORTER_VERSION).$${os}-$${arch}/node_exporter || { echo "extract failed for $$target"; exit 1; }; \
		chmod +x $$dest; \
		rm -f $$tgz; \
		echo "[node_exporter] staged $$dest"; \
	done
	@echo "[node_exporter] note: linux-only (upstream doesn't ship darwin in releases)"

.PHONY: fetch-process-exporter
fetch-process-exporter: ## [release] 下载 process-exporter 到 bin/<os>-<arch>/process_exporter (linux-only)
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/process_exporter; \
		if [ -f $$dest ]; then \
			echo "[process_exporter] $$dest already present — skip"; \
			continue; \
		fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/process_exporter-$$os-$$arch.tar.gz; \
		url=https://github.com/ncabatoff/process-exporter/releases/download/v$(PROCESS_EXPORTER_VERSION)/process-exporter-$(PROCESS_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[process_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { echo "process-exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz --strip-components=1 -C $(BIN_DIR)/$$target process-exporter-$(PROCESS_EXPORTER_VERSION).$${os}-$${arch}/process-exporter || { echo "extract failed for $$target"; exit 1; }; \
		mv $(BIN_DIR)/$$target/process-exporter $$dest; \
		chmod +x $$dest; \
		rm -f $$tgz; \
		echo "[process_exporter] staged $$dest"; \
	done
	@echo "[process_exporter] note: linux-only"

.PHONY: fetch-db-exporters fetch-mysqld-exporter fetch-postgres-exporter fetch-redis-exporter fetch-mongodb-exporter
fetch-db-exporters: fetch-mysqld-exporter fetch-postgres-exporter fetch-redis-exporter fetch-mongodb-exporter ## [release] 下载数据库 exporter 到 bin/<os>-<arch>/ (linux-only)

fetch-mysqld-exporter: ## [release] 下载 mysqld_exporter 到 bin/<os>-<arch>/mysqld_exporter
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/mysqld_exporter; \
		if [ -f $$dest ]; then echo "[mysqld_exporter] $$dest already present — skip"; continue; fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/mysqld_exporter-$$os-$$arch.tar.gz; tmpdir=$$(mktemp -d); \
		url=https://github.com/prometheus/mysqld_exporter/releases/download/v$(MYSQLD_EXPORTER_VERSION)/mysqld_exporter-$(MYSQLD_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[mysqld_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "mysqld_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $$tmpdir || { rm -rf $$tmpdir $$tgz; echo "extract failed for $$target"; exit 1; }; \
		found=$$(find $$tmpdir -type f -name mysqld_exporter -print -quit); \
		test -n "$$found" || { rm -rf $$tmpdir $$tgz; echo "mysqld_exporter binary missing in archive for $$target"; exit 1; }; \
		install -m 0755 "$$found" $$dest; \
		rm -rf $$tmpdir $$tgz; \
		echo "[mysqld_exporter] staged $$dest"; \
	done

fetch-postgres-exporter: ## [release] 下载 postgres_exporter 到 bin/<os>-<arch>/postgres_exporter
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/postgres_exporter; \
		if [ -f $$dest ]; then echo "[postgres_exporter] $$dest already present — skip"; continue; fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/postgres_exporter-$$os-$$arch.tar.gz; tmpdir=$$(mktemp -d); \
		url=https://github.com/prometheus-community/postgres_exporter/releases/download/v$(POSTGRES_EXPORTER_VERSION)/postgres_exporter-$(POSTGRES_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[postgres_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "postgres_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $$tmpdir || { rm -rf $$tmpdir $$tgz; echo "extract failed for $$target"; exit 1; }; \
		found=$$(find $$tmpdir -type f -name postgres_exporter -print -quit); \
		test -n "$$found" || { rm -rf $$tmpdir $$tgz; echo "postgres_exporter binary missing in archive for $$target"; exit 1; }; \
		install -m 0755 "$$found" $$dest; \
		rm -rf $$tmpdir $$tgz; \
		echo "[postgres_exporter] staged $$dest"; \
	done

fetch-redis-exporter: ## [release] 下载 redis_exporter 到 bin/<os>-<arch>/redis_exporter
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/redis_exporter; \
		if [ -f $$dest ]; then echo "[redis_exporter] $$dest already present — skip"; continue; fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/redis_exporter-$$os-$$arch.tar.gz; tmpdir=$$(mktemp -d); \
		url=https://github.com/oliver006/redis_exporter/releases/download/v$(REDIS_EXPORTER_VERSION)/redis_exporter-v$(REDIS_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[redis_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "redis_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $$tmpdir || { rm -rf $$tmpdir $$tgz; echo "extract failed for $$target"; exit 1; }; \
		found=$$(find $$tmpdir -type f -name redis_exporter -print -quit); \
		test -n "$$found" || { rm -rf $$tmpdir $$tgz; echo "redis_exporter binary missing in archive for $$target"; exit 1; }; \
		install -m 0755 "$$found" $$dest; \
		rm -rf $$tmpdir $$tgz; \
		echo "[redis_exporter] staged $$dest"; \
	done

fetch-mongodb-exporter: ## [release] 下载 mongodb_exporter 到 bin/<os>-<arch>/mongodb_exporter
	@for target in $(EDGE_PLUGIN_ARCHES); do \
		dest=$(BIN_DIR)/$$target/mongodb_exporter; \
		if [ -f $$dest ]; then echo "[mongodb_exporter] $$dest already present — skip"; continue; fi; \
		mkdir -p $(BIN_DIR)/$$target; \
		os=$${target%-*}; arch=$${target##*-}; \
		tgz=/tmp/mongodb_exporter-$$os-$$arch.tar.gz; tmpdir=$$(mktemp -d); \
		url=https://github.com/percona/mongodb_exporter/releases/download/v$(MONGODB_EXPORTER_VERSION)/mongodb_exporter-$(MONGODB_EXPORTER_VERSION).$${os}-$${arch}.tar.gz; \
		echo "[mongodb_exporter] downloading $$url"; \
		curl $(FETCH_CURL_FLAGS) -o $$tgz $$url || { rm -rf $$tmpdir; echo "mongodb_exporter download failed for $$target"; exit 1; }; \
		tar -xzf $$tgz -C $$tmpdir || { rm -rf $$tmpdir $$tgz; echo "extract failed for $$target"; exit 1; }; \
		found=$$(find $$tmpdir -type f -name mongodb_exporter -print -quit); \
		test -n "$$found" || { rm -rf $$tmpdir $$tgz; echo "mongodb_exporter binary missing in archive for $$target"; exit 1; }; \
		install -m 0755 "$$found" $$dest; \
		rm -rf $$tmpdir $$tgz; \
		echo "[mongodb_exporter] staged $$dest"; \
	done

# package deps deliberately exclude `build-linux` and `build-web`:
#   - build-linux produces a host-side opskeeper binary which dist/package.sh
#     never consumes (the manager binary inside opskeeper:VERSION docker
#     image is what's shipped; the host-side cross-compile was dead
#     code costing ~1-3 min per run).
#   - build-web produces web/dist/ which docker-build-web doesn't use
#     either — the web Dockerfile runs its own `pnpm install --frozen-lockfile &&
#     pnpm run build` inside the builder stage. Removing the host-side pnpm pass
#     saves another ~2-5 min per run.
# Run those targets manually if you need the host-side artefacts
# (e.g. for `make run-opskeeper` debugging).
.PHONY: build-edge-bundle
build-edge-bundle: ## [release] 打 ADR-024 edge upgrade bundle 到 dist/out/edge-bundles/
	@mkdir -p $(OUT)/edge-bundles
	@for arch in $(EDGE_PLUGIN_ARCHES); do \
		bash dist/build-edge-bundle.sh $(VERSION) $$arch $(OUT)/edge-bundles; \
	done

.PHONY: fetch-embedding-model
fetch-embedding-model: ## [release] 预拉 BGE 离线嵌入模型到 .cache/（幂等；package 会把它打进 tarball）
	bash dist/fetch-embedding-model.sh

.PHONY: check-release-target package package-all
check-release-target:
	@if [ "$(PLATFORM)" != "$(TARGET_OS)/$(TARGET_ARCH)" ]; then \
		echo "PLATFORM=$(PLATFORM) does not match TARGET_OS/TARGET_ARCH=$(TARGET_OS)/$(TARGET_ARCH)"; \
		echo "Use TARGET_ARCH=arm64 or PLATFORM=linux/arm64, but keep them consistent."; \
		exit 2; \
	fi
	@case "$(PACKAGE_TARGET)" in \
		linux-amd64|linux-arm64) ;; \
		*) echo "unsupported PACKAGE_TARGET=$(PACKAGE_TARGET); expected linux-amd64 or linux-arm64"; exit 2 ;; \
	esac

# Order matters: fetch-* / build-edge-all populate bin/ → docker-* bake
# the images → recipe-time we rebuild the edge bundle (because dist/out
# gets wiped first) and only then dist/package.sh assembles the
# release tarball that includes the bundle as a sibling of the per-arch
# edge binaries (ADR-024).
#
# NB: fetch-embedding-model is intentionally NOT a dep — pulling the BGE
# model is slow/brittle over CN networks, so it stays a one-off step.
# For offline RAG (OPSKEEPER_EMBEDDING_PROVIDER=local) run
# `make fetch-embedding-model` once before `make package`, otherwise
# dist/package.sh warns and ships a tarball without the model.
package: check-release-target fetch-promtail fetch-otelcol fetch-node-exporter fetch-process-exporter fetch-db-exporters build-edge-all docker-build docker-build-broker docker-build-web ## [release] 打单架构 release tarball 到 dist/out/（TARGET_ARCH 可覆盖）
	@if [ "$(PACKAGE_CLEAN)" = "1" ]; then rm -rf dist/stage dist/out; fi
	@mkdir -p dist/stage dist/out
	@$(MAKE) --no-print-directory build-edge-bundle
	PACKAGE_TARGET="$(PACKAGE_TARGET)" DOCKER_PLATFORM="$(PLATFORM)" bash dist/package.sh "$(VERSION)" "$(STAGE)" "$(OUT)"
	@echo ""
	@echo "=== release artefact ==="
	@ls -lh $(OUT)/opskeeper-$(VERSION)-$(PACKAGE_TARGET).tar.xz
	@if [ -f $(OUT)/opskeeper-$(VERSION)-$(PACKAGE_TARGET).tar.xz.sha256 ]; then \
		cat $(OUT)/opskeeper-$(VERSION)-$(PACKAGE_TARGET).tar.xz.sha256; \
	fi

package-all: ## [release] 打 amd64 + arm64 两个生产安装包到 dist/out/
	@rm -rf dist/stage dist/out
	@mkdir -p dist/stage dist/out
	@$(MAKE) --no-print-directory package TARGET_OS=linux TARGET_ARCH=amd64 PLATFORM=linux/amd64 PACKAGE_CLEAN=0
	@$(MAKE) --no-print-directory package TARGET_OS=linux TARGET_ARCH=arm64 PLATFORM=linux/arm64 PACKAGE_CLEAN=0
	@echo ""
	@echo "=== release artefacts ==="
	@ls -lh $(OUT)/opskeeper-$(VERSION)-linux-amd64.tar.xz $(OUT)/opskeeper-$(VERSION)-linux-arm64.tar.xz
	@for f in $(OUT)/opskeeper-$(VERSION)-linux-amd64.tar.xz.sha256 $(OUT)/opskeeper-$(VERSION)-linux-arm64.tar.xz.sha256; do \
		[ -f "$$f" ] && cat "$$f"; \
	done

.PHONY: dist-clean
dist-clean: ## [release] 清理 release 产物（dist/stage dist/out bin/<os>-*）
	rm -rf dist/stage dist/out $(BIN_DIR)/linux-* $(BIN_DIR)/darwin-* $(BIN_DIR)/windows-*

.PHONY: version-print
version-print: ## [release] 打印当前 VERSION（CI 消费用）
	@echo $(VERSION)

# ----------------------------------------------------------------------------
# clean
# ----------------------------------------------------------------------------

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR) coverage.out coverage.html
