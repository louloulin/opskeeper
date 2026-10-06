package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- 决策 321：入口接线的守卫 -------------------------------------------------
//
// 这一组守的是本仓最贵的那种失败：**「已审计」而运行时一行也没写**。
// SetAuditEvent 在没有槽时是 no-op，所以一个完美的 handler 可以完全合规地
// 什么都不记，而它上面的每一道闸门都还是绿的。cmd/higress-console 就是这样一个
// 进程——它从 srv.Routes() 直接起服务，中间件一个也没有。

// entryTree 造一棵带 cmd/ 的树。不能复用 main_test.go 的 tree()：那个把
// 每个相对路径重写到 core/manager/server/ 底下（它服务的是路由表，而本组
// 的文件在 cmd/），用它会把 cmd/thing/main.go 写成一个路由包里的 main.go。
func entryTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// onlyEntryPoints narrows the shipped process table to nothing, so a fixture
// tree is judged on its own terms. Without it every synthetic tree would
// report all five real binaries as deleted — the table describing the
// repository, not the tree under test.
func onlyEntryPoints(t *testing.T, entries ...EntryPoint) {
	t.Helper()
	saved := EntryPoints
	EntryPoints = entries
	t.Cleanup(func() { EntryPoints = saved })
}

// 1. 一个对外服务、没装槽、又没写理由的进程，必须报出来。
func TestAServingProcessWithNoSlotAndNoReasonFails(t *testing.T) {
	onlyEntryPoints(t, EntryPoint{File: "cmd/thing/main.go"})
	root := entryTree(t, map[string]string{
		"cmd/thing/main.go": `package main

func main() {
	srv := &http.Server{Addr: ":8080"}
	_ = srv
}
`,
	})
	missing, unlisted, stale := checkEntryPoints(root)
	if len(missing) != 1 || !strings.Contains(missing[0], "cmd/thing/main.go") {
		t.Fatalf("missing = %v, want the unslotted process", missing)
	}
	if !strings.Contains(missing[0], "no-op") {
		t.Errorf("the finding does not say what it costs: %q", missing[0])
	}
	if len(unlisted) != 0 || len(stale) != 0 {
		t.Errorf("unlisted = %v, stale = %v, want both empty", unlisted, stale)
	}
}

// 2. 装了槽的进程不需要理由。
func TestAServingProcessThatInstallsTheSlotIsQuiet(t *testing.T) {
	onlyEntryPoints(t)
	root := entryTree(t, map[string]string{
		"cmd/thing/main.go": `package main

func main() {
	mux := chi.NewRouter()
	mux.Use(middleware.AuditMiddleware(uc))
	_ = mux
}
`,
	})
	missing, unlisted, stale := checkEntryPoints(root)
	// It is unlisted (it is not in the shipped table), but it is NOT missing:
	// a process that installs the slot has nothing to declare.
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none for a process that installs the slot", missing)
	}
	if len(unlisted) != 1 {
		t.Fatalf("unlisted = %v, want the process reported as unjudged", unlisted)
	}
	_ = stale
}

// 3. 陈旧判定：进程长出了中间件，而表里还写着它没有——这条比漏报更坏，
// 因为它让读者相信一个已经不成立的结论。
func TestAVerdictThatContradictsTheProcessIsStale(t *testing.T) {
	root := entryTree(t, map[string]string{
		"cmd/thing/main.go": `package main

func main() {
	mux := chi.NewRouter()
	mux.Use(middleware.AuditMiddleware(uc))
	_ = mux
}
`,
	})
	onlyEntryPoints(t, EntryPoint{File: "cmd/thing/main.go", Slot: "洞：it has no slot"})
	missing, unlisted, stale := checkEntryPoints(root)
	if len(missing) != 0 || len(unlisted) != 0 {
		t.Fatalf("missing = %v, unlisted = %v, want both empty", missing, unlisted)
	}
	if len(stale) != 1 || !strings.Contains(stale[0], "no longer describes it") {
		t.Fatalf("stale = %v, want the contradicting verdict reported", stale)
	}
}

// 4. 一个没有 cmd/ 的夹具树不是「五个二进制全被删了」。
func TestAFixtureWithNoCmdDirectoryReportsNothing(t *testing.T) {
	root := tree(t, map[string]string{
		"widgets/http.go": "package widgets\n",
	})
	missing, unlisted, stale := checkEntryPoints(root)
	if len(missing) != 0 || len(unlisted) != 0 || len(stale) != 0 {
		t.Fatalf("missing=%v unlisted=%v stale=%v, want all empty on a tree with no cmd/",
			missing, unlisted, stale)
	}
}

// 5. 一次性的命令行工具不是对外服务进程：表里没有它们，也不该报。
func TestAOneShotToolIsNotAnEntryPoint(t *testing.T) {
	onlyEntryPoints(t)
	root := entryTree(t, map[string]string{
		"cmd/seed/main.go": `package main

func main() {
	db.Exec("insert ...")
}
`,
	})
	missing, unlisted, stale := checkEntryPoints(root)
	if len(missing) != 0 || len(unlisted) != 0 || len(stale) != 0 {
		t.Fatalf("missing=%v unlisted=%v stale=%v, want all empty", missing, unlisted, stale)
	}
}

// 6. 表与仓库一致：五个进程，逐个都有判定，且装的槽与判定相符。
func TestTheEntryPointTableAgreesWithTheRepository(t *testing.T) {
	root := "../.."
	missing, unlisted, stale := checkEntryPoints(root)
	if len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	if len(unlisted) != 0 {
		t.Fatalf("unlisted = %v", unlisted)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %v", stale)
	}
	// 决策 325 把这个断言反过来写了。原来是「确实有一个进程没装槽」——
	// 那条断言只能靠表格自己成立，而表格是手写的，于是它保护的是一个
	// 字符串而不是一个事实。查清 cmd/opskeeper-edge 的代码之后发现它只
	// 暴露两条只读路由，那个「洞」从来没有被代码支持过。
	//
	// 现在的断言是：**凡是记着「洞」的进程，它的 main.go 必须真的注册了
	// mutating 路由**。这条由 checkEntryPoints 在每次运行时对源码验证，
	// 上面的 stale 为空即已证明；这里再钉一次计数，是为了让「洞归零」
	// 这件事在报告里可读，而不是只能靠翻代码推。
	if got := countSlotGaps(); got != 0 {
		t.Fatalf("countSlotGaps() = %d, want 0: every acknowledged hole must be backed by a real mutating route in main.go", got)
	}
	if noSlot := countEntryPointsWithoutSlot(); noSlot == 0 {
		t.Fatal("countEntryPointsWithoutSlot() = 0; the settled exemptions (fixtures, read-only edge surface) are gone from the table")
	}
}

// --- 决策 325：「洞」必须由代码证明 --------------------------------------------
//
// 前四刀里最贵的一类是**量具被自己的表满足**。321 是「已审计」而运行时一行
// 都没写，324 是注释冒充已接线，325 更靠后：命令数「已承认的洞」时只读表格
// 里有没有「洞：」这个前缀，从不问那个进程注册了什么。于是任何人只要在
// 表里写上这两个字，就算「如实记录了一个洞」——而 edge 那个洞，代码从来
// 不支持。

// 10. 一个不注册任何 mutating 路由的进程，不能记「洞」。这条是本刀的正身：
// edge 的 main.go 只有 Handle("/metrics") 与 Get("/healthz")，而它挂着「洞」。
func TestAHoleVerdictMustBeBackedByAMutatingRoute(t *testing.T) {
	onlyEntryPoints(t, EntryPoint{
		File: "cmd/edge/main.go",
		Slot: "洞：节点面同样没有装槽",
	})
	root := entryTree(t, map[string]string{
		"cmd/edge/main.go": `package main

func main() {
	mux := chi.NewRouter()
	mux.Handle("/metrics", prom.Handler(reg))
	mux.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	_ = mux
}
`,
	})
	missing, unlisted, stale := checkEntryPoints(root)
	if len(missing) != 0 || len(unlisted) != 0 {
		t.Fatalf("missing = %v, unlisted = %v, want both empty", missing, unlisted)
	}
	if len(stale) != 1 {
		t.Fatalf("stale = %v, want the unsupported hole reported", stale)
	}
	// 报告必须说出代码里实际注册了什么，否则「你记错了」这句话本身
	// 就是一次猜测，读者没法自己核对。
	for _, want := range []string{"Get", "/healthz", "Handle", "/metrics"} {
		if !strings.Contains(stale[0], want) {
			t.Errorf("stale finding does not show the actual surface (%s): %q", want, stale[0])
		}
	}
}

// 11. 反向：注册了 mutating 路由却不记洞，是漏报，而漏报是这个命令存在的
// 理由。**装不装中间件是另一道闸门的事，这一道问的是「你的说法有没有依据」**——
// 一个自称「无需槽」却在 main.go 里 POST 的进程，恰恰是最危险的那种表。
func TestAMutatingRouteWithASettledVerdictIsStale(t *testing.T) {
	onlyEntryPoints(t, EntryPoint{File: "cmd/edge/main.go", Slot: "无需槽：只读"})
	root := entryTree(t, map[string]string{
		"cmd/edge/main.go": `package main

func main() {
	mux := chi.NewRouter()
	mux.Post("/v1/packages", h.install)
	_ = mux
}
`,
	})
	_, _, stale := checkEntryPoints(root)
	if len(stale) != 1 {
		t.Fatalf("stale = %v, want the unregistered mutation reported", stale)
	}
	for _, want := range []string{"/v1/packages", "h.install", "洞："} {
		if !strings.Contains(stale[0], want) {
			t.Errorf("stale finding %q does not mention %s", stale[0], want)
		}
	}
}

// 12. 装了槽的进程不参与这道对账：它的请求带槽，写不写由路由表那把尺子判。
// 这一条挡住的是「把对账加得太宽」——宽到把已经装了槽的进程也拿 mutating
// 路由去质问，那就不是更严的闸门，是一把坏尺子。
//
// **第一版这条用例是空转的**：它给的EntryPoint 只有一个空 Slot，于是
// 无论对账的 `!installs` 前置条件在不在，它都同样不触发，删掉前置条件它
// 照样全绿。变异验证撞上这个之后才改成现在这个形状——带理由、装了槽、
// 又注册了 mutating 路由：正确行为是 1 条 stale（早先那条「它现在装了槽」
// 的规则），去掉前置条件就变成 2 条。差一条，就分得开。
func TestASlottedProcessIsNotJudgedOnTheSurfaceCrossCheck(t *testing.T) {
	onlyEntryPoints(t, EntryPoint{File: "cmd/ops/main.go", Slot: "无需槽：曾经记过"})
	root := entryTree(t, map[string]string{
		"cmd/ops/main.go": `package main

func main() {
	mux := chi.NewRouter()
	mux.Use(middleware.AuditMiddleware(uc))
	mux.Post("/v1/things", h.create)
	_ = mux
}
`,
	})
	missing, unlisted, stale := checkEntryPoints(root)
	if len(missing) != 0 || len(unlisted) != 0 {
		t.Fatalf("missing = %v, unlisted = %v, want both empty", missing, unlisted)
	}
	if len(stale) != 1 || !strings.Contains(stale[0], "no longer describes it") {
		t.Fatalf("stale = %v, want exactly the pre-existing 'it installs the slot now' finding and nothing from the surface cross-check", stale)
	}
}

// 13. 注释里写一条 POST 不算注册。321/324 已经把「注释冒充接线」修掉了，
// 这一条是它的第三条腿：**注释也不能冒充一个洞**。否则把 edge 的「洞」
// 从理由改写成「这里其实有个 Post」就能骗过对账。
func TestAMutatingRouteInACommentDoesNotCreateAHole(t *testing.T) {
	onlyEntryPoints(t, EntryPoint{File: "cmd/edge/main.go", Slot: "洞：曾经有"})
	root := entryTree(t, map[string]string{
		"cmd/edge/main.go": `package main

func main() {
	mux := chi.NewRouter()
	// mux.Post("/v1/removed", h.gone) was deleted in 决策 318.
	mux.Get("/healthz", ok)
	_ = mux
}
`,
	})
	_, _, stale := checkEntryPoints(root)
	if len(stale) != 1 {
		t.Fatalf("stale = %v, want exactly the unsupported hole", stale)
	}
	if strings.Contains(stale[0], "/v1/removed") {
		t.Errorf("a comment was read as a route: %q", stale[0])
	}
}

// --- 决策 323：注释不是代码 ---------------------------------------------------
//
// 写完「为什么把闭包提成具名函数」那段注释之后，这个命令把注释里那一行
// `protected.Delete("/v1/pages/{id}", func(...))` 读成了一条真的路由。修法
// 有两个：删掉注释，或者让闸门别看注释。**删注释是错的**——那段注释是这个文件
// 里最有用的东西，而一个会因为你写文档而失败的闸门只会让人把文档删掉。

// 7. 注释里的注册样板不是路由。这一条必须真的走 Run()：早先的版本只查了
// checkEntryPoints 与 packageBodies，两者都不扫描路由，于是把 stripComments
// 改成什么都不做之后，这条用例仍然是绿的——**它测的不是它说自己测的东西**。
func TestARouteExampleInACommentIsNotARoute(t *testing.T) {
	root := tree(t, map[string]string{
		"widgets/http.go": `package widgets

// Before the change it read:
//   r.Delete("/v1/pages/{id}", func(w http.ResponseWriter, r *http.Request) {
//       w.WriteHeader(204)
//   })
func (h *Handler) Register(r chi.Router) {
	r.Delete("/v1/widgets/{id}", h.drop)
}
func (h *Handler) drop(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }
`,
	})
	with := EntryPoints
	defer func() { EntryPoints = with }()
	EntryPoints = nil
	res := Run(root)
	if len(res.Missing) != 1 {
		t.Fatalf("missing = %v, want exactly the one real route", res.Missing)
	}
	if !strings.Contains(res.Missing[0], "/v1/widgets/{id}") {
		t.Errorf("missing = %q, want the real route", res.Missing[0])
	}
	for _, m := range res.Missing {
		if strings.Contains(m, "/v1/pages/") {
			t.Errorf("a comment was read as a route: %q", m)
		}
	}
}

// 8. 而注释里的 URL 不会把后面的代码吃掉。
func TestACommentStripperDoesNotSwallowStrings(t *testing.T) {
	src := `package p

// see the router table below
var base = "https://example.com/docs" // and this is a trailing note
var route = "/v1/things"
var raw = ` + "`" + `a /* not a comment */ b` + "`" + `
func f() { router.Delete("/v1/things", h.drop) }
`
	got := stripComments(src)
	if !strings.Contains(got, "https://example.com/docs") {
		t.Error("a // inside a string literal started a comment")
	}
	if !strings.Contains(got, "router.Delete(") {
		t.Error("the real registration was eaten")
	}
	if !strings.Contains(got, "not a comment") {
		t.Error("/* inside a raw string literal was treated as a comment")
	}
	if strings.Contains(got, "trailing note") {
		t.Error("a trailing line comment survived")
	}
}

// 9. 注释不能冒充接线。决策 324 把 AuditMiddleware 挂进 cmd/higress-console
// 时，那一行的注释里就写着这个中间件的名字——而闸门读原始文本，于是把中间件
// 删掉之后，一句解释它存在的注释仍然让这个进程「看起来已接线」。
// **一个能被文档满足的闸门，比一个数错的闸门更坏**：它让人去写解释而不是去接线。
func TestAMiddlewareMentionedOnlyInACommentDoesNotCountAsWired(t *testing.T) {
	root := entryTree(t, map[string]string{
		"cmd/thing/main.go": `package main

func main() {
	mux := chi.NewRouter()
	// AuditMiddleware installs the slot; we used to mount it here.
	_ = mux
	httpSrv := &http.Server{Addr: ":8080"}
	_ = httpSrv
}
`,
	})
	onlyEntryPoints(t, EntryPoint{File: "cmd/thing/main.go"})
	missing, _, _ := checkEntryPoints(root)
	if len(missing) != 1 || !strings.Contains(missing[0], "cmd/thing/main.go") {
		t.Fatalf("missing = %v, want the process reported as unwired despite the comment", missing)
	}
}
