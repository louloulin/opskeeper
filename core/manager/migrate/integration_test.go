package migrate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/manager/migrate"
	"github.com/vincent-wuhan/opskeeper/core/manager/migrate/clients"
)

// mockOpsKeeper 模拟 ops-keeper HTTP API。
//
// 实现 GET /api/v1/{entity} 与 GET /healthz。
// 存储用 sync.Map 模拟；统计调用次数便于断言。
type mockOpsKeeper struct {
	mu    sync.Mutex
	store map[string][]map[string]any
	hits  int64
	delay time.Duration // 模拟慢源
}

func newMockOpsKeeper(seed map[string][]map[string]any) *mockOpsKeeper {
	return &mockOpsKeeper{store: seed}
}

func (m *mockOpsKeeper) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&m.hits, 1)
		if m.delay > 0 {
			time.Sleep(m.delay)
		}
		// 路径: /api/v1/{entity}
		entity := r.URL.Path[len("/api/v1/"):]
		m.mu.Lock()
		rows := m.store[entity]
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":             rows,
			"opskeeper_version": "v0.0.1-mock",
		})
	})
	return mux
}

// mockOpskeeper 模拟 opskeeper HTTP API。
//
// 记录创建的实体（用于幂等校验）；支持 by-source-id 查询。
type mockOpskeeper struct {
	mu           sync.Mutex
	byType       map[string][]map[string]any // type -> rows（含 source_id + id）
	createdTotal int64
	deletedTotal int64
	checksTotal  int64

	// deleteOrder 记录 DELETE 到达的顺序，用于断言回滚按依赖的逆序删。
	deleteOrder []string
}

func newMockOpskeeper() *mockOpskeeper {
	return &mockOpskeeper{byType: make(map[string][]map[string]any)}
}

func (m *mockOpskeeper) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	// GET /api/v1/{entity}/by-source-id/{id}
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&m.checksTotal, 1)
		path := r.URL.Path[len("/api/v1/"):]
		// 拆 entity 与子路径
		var entity, subpath string
		for i := 0; i < len(path); i++ {
			if path[i] == '/' {
				entity = path[:i]
				subpath = path[i+1:]
				break
			}
		}
		if entity == "" {
			entity = path // 没有子路径的情况，如 /api/v1/users
		}
		if entity == "" {
			http.NotFound(w, r)
			return
		}
		// query string 中提取 type（middleware_resources 共享 endpoint）
		sourceType := r.URL.Query().Get("type")
		effEntity := entity
		if sourceType != "" {
			effEntity = sourceType
		}
		// GET: by-source-id/{id}; POST: 直接在 entity 路径
		switch r.Method {
		case http.MethodGet:
			if subpath == "" {
				http.NotFound(w, r)
				return
			}
			// 期望 subpath = "by-source-id/{id}"
			if len(subpath) > 13 && subpath[:13] == "by-source-id/" {
				srcID := subpath[13:]
				m.mu.Lock()
				for _, row := range m.byType[effEntity] {
					if fmt.Sprintf("%v", row["source_id"]) == srcID {
						m.mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(row)
						return
					}
				}
				m.mu.Unlock()
				http.NotFound(w, r)
				return
			}
			http.NotFound(w, r)
		case http.MethodPost:
			// 创建
			body := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			m.mu.Lock()
			atomic.AddInt64(&m.createdTotal, 1)
			id := fmt.Sprintf("%s-%d", effEntity, m.createdTotal)
			body["id"] = id
			m.byType[effEntity] = append(m.byType[effEntity], body)
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"id": id},
			})
		case http.MethodDelete:
			// 回滚：按 /api/v1/{entity}/{id} 删掉一条。
			id := subpath
			m.mu.Lock()
			defer m.mu.Unlock()
			m.deleteOrder = append(m.deleteOrder, effEntity)
			rows := m.byType[effEntity]
			for i, row := range rows {
				if fmt.Sprintf("%v", row["id"]) == id {
					m.byType[effEntity] = append(rows[:i], rows[i+1:]...)
					atomic.AddInt64(&m.deletedTotal, 1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

// helper: 启动一对 mock servers。
type mockPair struct {
	OpsKeeper  *httptest.Server
	Opskeeper  *httptest.Server
	OpsKeeperH *mockOpsKeeper
	OpskeeperH *mockOpskeeper
}

func startMocks(t *testing.T, seed map[string][]map[string]any) *mockPair {
	t.Helper()
	ok := newMockOpsKeeper(seed)
	og := newMockOpskeeper()
	okSrv := httptest.NewServer(ok.Handler())
	ogSrv := httptest.NewServer(og.Handler())
	t.Cleanup(func() {
		okSrv.Close()
		ogSrv.Close()
	})
	return &mockPair{
		OpsKeeper:  okSrv,
		Opskeeper:  ogSrv,
		OpsKeeperH: ok,
		OpskeeperH: og,
	}
}

func TestIntegration_ExportImportRoundtrip(t *testing.T) {
	seed := map[string][]map[string]any{
		"users": {
			{"id": 1, "email": "alice@example.com", "name": "Alice", "project_id": 42},
			{"id": 2, "email": "bob@example.com", "name": "Bob", "project_id": 100},
		},
		"projects": {
			{"id": 42, "name": "ops-prod", "owner_id": 1, "project_id": 42},
			{"id": 100, "name": "ops-staging", "owner_id": 2, "project_id": 100},
		},
		"pg_connections": {
			{"id": 1, "project_id": 42, "name": "prod-pg-1", "host": "pg-1", "port": 5432, "database": "orders", "username": "u", "password": "p"},
		},
	}
	mocks := startMocks(t, seed)
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snap.json")

	ctx := context.Background()

	// Export
	snap, err := migrate.Export(ctx, migrate.ExportOptions{
		Output: snapshotPath,
		Source: mocks.OpsKeeper.URL,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if snap.TotalRows() != 5 {
		t.Errorf("TotalRows=%d want 5", snap.TotalRows())
	}

	// Import
	result, err := migrate.Import(ctx, migrate.ImportOptions{
		Snapshot:      snapshotPath,
		Target:        mocks.Opskeeper.URL,
		TenantMapping: "42=1,100=2",
		RatePerSec:    1000,
		Entities:      importableEntities(),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	// 快照里 5 行，其中 pg_connections 那一行落在没有写入端点的实体上，
	// 所以只搬得过去 4 行。这不是数据问题：这个 mock 会给任何路径回
	// 201，而真实的 manager 根本没有 /api/v1/middleware_resources，
	// 决策 291 把这一类挡在写入之前了。
	if result.Imported != 4 {
		t.Errorf("Imported=%d want 4", result.Imported)
	}
	if result.Failed != 0 {
		t.Errorf("Failed=%d want 0 (failures=%v)", result.Failed, result.Failures)
	}

	// Opskeeper mock 应记录 4 个创建 + 4 个 by-source-id 查询
	if mocks.OpskeeperH.createdTotal != 4 {
		t.Errorf("opskeeper created=%d want 4", mocks.OpskeeperH.createdTotal)
	}
	if mocks.OpskeeperH.checksTotal < 4 {
		t.Errorf("opskeeper checks=%d want >= 4", mocks.OpskeeperH.checksTotal)
	}
}

func TestIntegration_Idempotency(t *testing.T) {
	seed := map[string][]map[string]any{
		"users": {
			{"id": 1, "email": "alice@example.com", "project_id": 42},
		},
	}
	mocks := startMocks(t, seed)
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snap.json")

	ctx := context.Background()
	_, err := migrate.Export(ctx, migrate.ExportOptions{
		Output: snapshotPath,
		Source: mocks.OpsKeeper.URL,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	opts := migrate.ImportOptions{
		Snapshot:      snapshotPath,
		Target:        mocks.Opskeeper.URL,
		TenantMapping: "42=1",
		RatePerSec:    1000,
		Entities:      []migrate.EntityType{migrate.EntityUsers},
	}

	// 第一次 import
	r1, err := migrate.Import(ctx, opts)
	if err != nil {
		t.Fatalf("Import #1: %v", err)
	}
	if r1.Imported != 1 {
		t.Errorf("Import #1 imported=%d want 1", r1.Imported)
	}

	// 第二次 import 应跳过（已存在）
	r2, err := migrate.Import(ctx, opts)
	if err != nil {
		t.Fatalf("Import #2: %v", err)
	}
	if r2.Imported != 0 {
		t.Errorf("Import #2 imported=%d want 0 (idempotent)", r2.Imported)
	}
	if r2.Skipped != 1 {
		t.Errorf("Import #2 skipped=%d want 1", r2.Skipped)
	}

	// opskeeper 应只创建 1 个
	if mocks.OpskeeperH.createdTotal != 1 {
		t.Errorf("opskeeper created=%d want 1 (idempotent)", mocks.OpskeeperH.createdTotal)
	}
}

func TestIntegration_TenantMapping_Undeclared(t *testing.T) {
	seed := map[string][]map[string]any{
		"users": {
			{"id": 1, "project_id": 999, "email": "x@x"},
		},
	}
	mocks := startMocks(t, seed)
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snap.json")
	ctx := context.Background()

	_, err := migrate.Export(ctx, migrate.ExportOptions{
		Output: snapshotPath,
		Source: mocks.OpsKeeper.URL,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// tenant_mapping 不包含 999 → 失败
	result, err := migrate.Import(ctx, migrate.ImportOptions{
		Snapshot:      snapshotPath,
		Target:        mocks.Opskeeper.URL,
		TenantMapping: "42=1",
		RatePerSec:    1000,
		Entities:      []migrate.EntityType{migrate.EntityUsers},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("Failed=%d want 1 (undeclared tenant)", result.Failed)
	}
	if mocks.OpskeeperH.createdTotal != 0 {
		t.Errorf("opskeeper created=%d want 0 (tenant blocked)", mocks.OpskeeperH.createdTotal)
	}
}

func TestIntegration_DryRun(t *testing.T) {
	seed := map[string][]map[string]any{
		"users": {
			{"id": 1, "project_id": 42, "email": "x@x"},
		},
	}
	mocks := startMocks(t, seed)
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snap.json")
	ctx := context.Background()

	_, _ = migrate.Export(ctx, migrate.ExportOptions{
		Output: snapshotPath,
		Source: mocks.OpsKeeper.URL,
	})

	result, err := migrate.Import(ctx, migrate.ImportOptions{
		Snapshot:      snapshotPath,
		Target:        mocks.Opskeeper.URL,
		TenantMapping: "42=1",
		RatePerSec:    1000,
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("Import dry-run: %v", err)
	}
	if result.Imported != 1 {
		t.Errorf("Dry-run imported=%d want 1", result.Imported)
	}
	if mocks.OpskeeperH.createdTotal != 0 {
		t.Errorf("Dry-run must not create: created=%d", mocks.OpskeeperH.createdTotal)
	}
}

func TestIntegration_VerifyAfterImport(t *testing.T) {
	seed := map[string][]map[string]any{
		"users": {
			{"id": 1, "project_id": 42, "email": "a@x"},
			{"id": 2, "project_id": 42, "email": "b@x"},
		},
		"projects": {
			{"id": 42, "name": "p1", "owner_id": 1, "project_id": 42},
		},
	}
	mocks := startMocks(t, seed)
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snap.json")
	ctx := context.Background()

	_, _ = migrate.Export(ctx, migrate.ExportOptions{
		Output: snapshotPath,
		Source: mocks.OpsKeeper.URL,
	})
	_, err := migrate.Import(ctx, migrate.ImportOptions{
		Snapshot:      snapshotPath,
		Target:        mocks.Opskeeper.URL,
		TenantMapping: "42=1",
		RatePerSec:    1000,
		Entities:      importableEntities(),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Verify
	result, err := migrate.Verify(ctx, migrate.VerifyOptions{
		SnapshotPath:  snapshotPath,
		Target:        mocks.Opskeeper.URL,
		TenantMapping: "42=1",
		Entities:      importableEntities(),
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.TotalSource != 3 {
		t.Errorf("TotalSource=%d want 3", result.TotalSource)
	}
	totalMatched := 0
	for _, n := range result.MatchedBySourceID {
		totalMatched += n
	}
	if totalMatched != 3 {
		t.Errorf("matched=%d want 3", totalMatched)
	}
	if len(result.MissingInTarget) != 0 {
		t.Errorf("MissingInTarget=%d want 0", len(result.MissingInTarget))
	}
}

func TestIntegration_Clients_Direct(t *testing.T) {
	// 直接测试两个客户端，不经 migrate 高层
	seed := map[string][]map[string]any{
		"users": {
			{"id": 1, "email": "x@x"},
			{"id": 2, "email": "y@y"},
		},
	}
	mocks := startMocks(t, seed)
	ctx := context.Background()

	ok := clients.NewOpsKeeperClient(mocks.OpsKeeper.URL, "")
	rows, err := ok.ListAll(ctx, "users")
	if err != nil {
		t.Fatalf("OpsKeeper.ListAll: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("rows=%d want 2", len(rows))
	}

	og := clients.NewTargetClient(mocks.Opskeeper.URL, "")
	if err := og.HealthCheck(ctx); err != nil {
		t.Fatalf("Opskeeper.HealthCheck: %v", err)
	}
}

// importableEntities 是这一组集成测试真正搬得过去的那几类。
//
// 它问的是注册表而不是抄一份名字，所以 opskeeper 侧一旦真的有了连接配置
// 或巡检计划的写入端点，这些用例会跟着把新端点也走一遍，而不用改测试。
func importableEntities() []migrate.EntityType { return migrate.ImportableEntities() }

// TestIntegration_RollbackRemovesWhatImportCreated 是这条链路的端到端证据。
//
// 决策 292 之前这条链是断的：import 把新建的 ID 攒在 result.CreatedIDs 里，
// 从不写进任何文件，而 rollback 只从文件里读——所以 `opskeeper-migrate rollback`
// 永远读到 0 行，报一句"总计: 0"然后成功退出。一个永远删不掉任何东西的回滚
// 命令，与没有回滚命令的区别只在于它让人以为有。
func TestIntegration_RollbackRemovesWhatImportCreated(t *testing.T) {
	seed := map[string][]map[string]any{
		"users": {
			{"id": 1, "project_id": 42, "email": "alice@example.com", "name": "Alice"},
			{"id": 2, "project_id": 42, "email": "bob@example.com", "name": "Bob"},
		},
		"projects": {
			{"id": 42, "project_id": 42, "name": "ops-prod"},
		},
	}
	mocks := startMocks(t, seed)
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "snap.json")
	ctx := context.Background()

	if _, err := migrate.Export(ctx, migrate.ExportOptions{
		Output: snapshotPath,
		Source: mocks.OpsKeeper.URL,
	}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	res, err := migrate.Import(ctx, migrate.ImportOptions{
		Snapshot:      snapshotPath,
		Target:        mocks.Opskeeper.URL,
		TenantMapping: "42=1",
		RatePerSec:    1000,
		Entities:      []migrate.EntityType{migrate.EntityUsers, migrate.EntityProjects},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Imported != 3 {
		t.Fatalf("Imported=%d want 3", res.Imported)
	}
	if res.RollbackSnapshot == "" {
		t.Fatal("Import wrote 3 rows but produced no rollback snapshot; " +
			"those 3 rows can now never be removed by this tool")
	}
	if _, err := os.Stat(res.RollbackSnapshot); err != nil {
		t.Fatalf("the rollback snapshot is not on disk: %v", err)
	}
	if mocks.OpskeeperH.deletedTotal != 0 {
		t.Fatalf("Import deleted %d rows; import must not delete", mocks.OpskeeperH.deletedTotal)
	}

	rb, err := migrate.Rollback(ctx, migrate.RollbackOptions{
		SnapshotPath: res.RollbackSnapshot,
		Target:       mocks.Opskeeper.URL,
	})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rb.Total != 3 || rb.Deleted != 3 || rb.Failed != 0 {
		t.Errorf("Rollback total=%d deleted=%d failed=%v want 3/3/0",
			rb.Total, rb.Deleted, rb.Failures)
	}
	if mocks.OpskeeperH.deletedTotal != 3 {
		t.Errorf("opskeeper deleted=%d want 3", mocks.OpskeeperH.deletedTotal)
	}

	// 逆序：projects 依赖 users，先删 projects。
	if len(mocks.OpskeeperH.deleteOrder) != 3 {
		t.Fatalf("deleteOrder=%v", mocks.OpskeeperH.deleteOrder)
	}
	if mocks.OpskeeperH.deleteOrder[0] != "orgs" {
		t.Errorf("rollback deleted %s first; orgs depends on users so it must go first, got %v",
			mocks.OpskeeperH.deleteOrder[0], mocks.OpskeeperH.deleteOrder)
	}
}

// TestARollbackSnapshotIsNeverOverwritten 钉住文件名冲突的处理。
//
// 秒级时间戳在两次导入落在同一秒时会撞名，而被覆盖的那一份正是"刚刚写进生产
// 的数据的删除清单"。决策 292 之前的 SaveRollbackSnapshot 直接写同名文件。
func TestARollbackSnapshotIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 3; i++ {
		snap := migrate.BuildRollbackSnapshot("http://target", map[migrate.EntityType][]string{
			migrate.EntityUsers: {"u-1"},
		})
		path, err := migrate.SaveRollbackSnapshot(snap, dir)
		if err != nil {
			t.Fatalf("SaveRollbackSnapshot #%d: %v", i, err)
		}
		paths = append(paths, path)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Fatalf("two rollback snapshots share the path %s; the second overwrote the first", p)
		}
		seen[p] = true
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
	}
}
