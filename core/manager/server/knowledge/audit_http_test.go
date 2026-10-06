package knowledge

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	auditport "github.com/vincent-wuhan/opskeeper/core/base/pkg/audit"
	"github.com/vincent-wuhan/opskeeper/core/base/pkg/tenantctx"
)

// --- 决策 343：知识文档五条写路由上宿主链 -------------------------------------
//
// 这一族的后果不在链上，而在**模型的后续输出里**：知识库进 RAG，一份写错的
// 文档影响此后每一次 AI 回答，而链上只有写它的那一行。所以这些断言盯的不是
// 「调了 SetAuditEvent」，而是「链上留下的那点信息够不够事后重建」——正文绝不
// 进链，进链的是它的身份。
//
// 此前这一族一个审计测试都没有，五条写路由从未被验证过任何审计性质。

// auditReq 发一次请求并把宿主链上的那一行读回来。槽位是宿主中间件挂的，
// 所以测试自己装——和 domains 侧那一族用同一个手法。
func auditReq(t *testing.T, router http.Handler, method, path, contentType string, body io.Reader) (*httptest.ResponseRecorder, auditport.Event, bool) {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	r = r.WithContext(tenantctx.With(r.Context(), tenantctx.Tenant{UserID: 42, Role: tenantctx.RoleAdmin}))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	r = r.WithContext(auditport.WithSlot(r.Context()))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, r)
	ev, set := auditport.GetAuditEvent(r.Context())
	return rec, ev, set
}

func docPayload(t *testing.T, ev auditport.Event) map[string]any {
	t.Helper()
	p, ok := ev.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is %T, want map", ev.Payload)
	}
	return p
}

// mustKey 是本族的主力断言：键**存在**与键的**值**是两件事。缺键在扫链的人
// 眼里等于「没人看过这份文档」，而链没有行可以补。
func mustKey(t *testing.T, p map[string]any, key string) any {
	t.Helper()
	v, ok := p[key]
	if !ok {
		t.Fatalf("payload missing key %q (payload=%v)", key, p)
	}
	return v
}

func seedDoc(t *testing.T, router http.Handler, title, content, path string, tags []string) uint64 {
	t.Helper()
	rec := jsonReq(t, router, http.MethodPost, "/v1/knowledge/docs", map[string]any{
		"title": title, "content": content, "path": path, "tags": tags,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed create: %d (%s)", rec.Code, rec.Body.String())
	}
	return decodeDoc(t, rec).ID
}

func TestAudit_KnowledgeDocCreate(t *testing.T) {
	router, _ := newE2E(t)
	rec, ev, set := auditReq(t, router, http.MethodPost, "/v1/knowledge/docs", "application/json",
		bytes.NewReader([]byte(`{"title":"nginx 重启 SOP","content":"systemctl restart nginx","path":"网络/HTTP","tags":["nginx"]}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d (%s)", rec.Code, rec.Body.String())
	}
	if !set {
		t.Fatal("create: no audit row")
	}
	if ev.Action != auditport.ActionKnowledgeDocCreate || ev.ResourceType != auditport.ResourceKnowledgeDoc {
		t.Fatalf("create: action=%s resource=%s", ev.Action, ev.ResourceType)
	}
	if ev.Status != auditport.StatusSuccess {
		t.Fatalf("create: status=%s", ev.Status)
	}
	if ev.ResourceName != "nginx 重启 SOP" {
		t.Fatalf("create: resource name=%q", ev.ResourceName)
	}
	if ev.ResourceID == "" || ev.ResourceID == "0" {
		t.Fatalf("create: resource id=%q", ev.ResourceID)
	}
	p := docPayload(t, ev)
	if got := mustKey(t, p, "content_chars"); got != len("systemctl restart nginx") {
		t.Fatalf("create: content_chars=%v", got)
	}
	if got := mustKey(t, p, "path"); got != "网络/HTTP" {
		t.Fatalf("create: path=%v", got)
	}
	// 正文不进链——它是文档本身，而且可能很大。
	for _, k := range []string{"content", "text", "body"} {
		if _, ok := p[k]; ok {
			t.Fatalf("create: payload must not carry %q", k)
		}
	}
}

func TestAudit_KnowledgeDocUpload(t *testing.T) {
	router, _ := newE2E(t)
	content := "# 重启手册\n\nsystemctl restart nginx\n"
	ct, body := buildUpload(t, "重启手册.md", content, "网络", "nginx,sop")
	rec, ev, set := auditReq(t, router, http.MethodPost, "/v1/knowledge/upload", ct, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d (%s)", rec.Code, rec.Body.String())
	}
	if !set {
		t.Fatal("upload: no audit row")
	}
	if ev.Action != auditport.ActionKnowledgeDocUpload {
		t.Fatalf("upload: action=%s", ev.Action)
	}
	p := docPayload(t, ev)
	// 这两个是不同的量：传进来多少字节、抽出多少字符。缺任一个都答不出
	// 「这份知识到底有多大」——对 pdf/docx 而言两者差得很远。
	if got := mustKey(t, p, "uploaded_bytes"); got != len(content) {
		t.Fatalf("upload: uploaded_bytes=%v want %d", got, len(content))
	}
	if _, ok := mustKey(t, p, "extracted_chars").(int); !ok {
		t.Fatalf("upload: extracted_chars not an int: %v", p["extracted_chars"])
	}
	if got := mustKey(t, p, "filename"); got != "重启手册.md" {
		t.Fatalf("upload: filename=%v", got)
	}
}

func TestAudit_KnowledgeDocUpdate(t *testing.T) {
	router, _ := newE2E(t)
	id := seedDoc(t, router, "SOP v1", "restart nginx", "网络", []string{"nginx"})

	// 一次没改正文的编辑：三个 *_changed 键**在 false 时也必须在**。
	rec, ev, set := auditReq(t, router, http.MethodPatch, "/v1/knowledge/docs/"+idStr(id), "application/json",
		bytes.NewReader([]byte(`{"title":"SOP v1","content":"restart nginx","path":"网络"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d (%s)", rec.Code, rec.Body.String())
	}
	if !set || ev.Action != auditport.ActionKnowledgeDocUpdate {
		t.Fatalf("update: set=%v action=%s", set, ev.Action)
	}
	p := docPayload(t, ev)
	for _, k := range []string{"content_changed", "title_changed"} {
		if v := mustKey(t, p, k); v != false {
			t.Fatalf("update: %s=%v want false", k, v)
		}
	}
	if got := mustKey(t, p, "title_before"); got != "SOP v1" {
		t.Fatalf("update: title_before=%v", got)
	}
	if got := mustKey(t, p, "path_before"); got != "网络" {
		t.Fatalf("update: path_before=%v", got)
	}

	// 真改：flags 翻过来，且记的是更新之前那份的规模。
	rec, ev, _ = auditReq(t, router, http.MethodPatch, "/v1/knowledge/docs/"+idStr(id), "application/json",
		bytes.NewReader([]byte(`{"title":"SOP v2","content":"systemctl reload nginx now","path":"网络/HTTP"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update2: %d (%s)", rec.Code, rec.Body.String())
	}
	p = docPayload(t, ev)
	if mustKey(t, p, "content_changed") != true || mustKey(t, p, "title_changed") != true {
		t.Fatalf("update2: flags=%v", p)
	}
	if got := mustKey(t, p, "chars_before"); got != len("restart nginx") {
		t.Fatalf("update2: chars_before=%v", got)
	}
	if got := mustKey(t, p, "path_after"); got != "网络/HTTP" {
		t.Fatalf("update2: path_after=%v", got)
	}
}

func TestAudit_KnowledgeDocMove(t *testing.T) {
	router, _ := newE2E(t)
	id := seedDoc(t, router, "SOP", "body", "网络/HTTP", nil)

	rec, ev, set := auditReq(t, router, http.MethodPatch, "/v1/knowledge/docs/"+idStr(id)+"/move", "application/json",
		bytes.NewReader([]byte(`{"path":"数据库/PG"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d (%s)", rec.Code, rec.Body.String())
	}
	if !set || ev.Action != auditport.ActionKnowledgeDocMove {
		t.Fatalf("move: set=%v action=%s", set, ev.Action)
	}
	p := docPayload(t, ev)
	// 路径决定检索过滤：一份被移走的文档从此在某些查询里消失。链上只有终点
	// 就答不出「它原来在哪」。
	if got := mustKey(t, p, "path_before"); got != "网络/HTTP" {
		t.Fatalf("move: path_before=%v", got)
	}
	if got := mustKey(t, p, "path_after"); got != "数据库/PG" {
		t.Fatalf("move: path_after=%v", got)
	}
	if mustKey(t, p, "moved") != true {
		t.Fatalf("move: moved=%v", p["moved"])
	}
}

func TestAudit_KnowledgeDocDelete(t *testing.T) {
	router, _ := newE2E(t)
	id := seedDoc(t, router, "会被删掉的 SOP", "body", "网络", []string{"nginx"})

	rec, ev, set := auditReq(t, router, http.MethodDelete, "/v1/knowledge/docs/"+idStr(id), "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d (%s)", rec.Code, rec.Body.String())
	}
	if !set || ev.Action != auditport.ActionKnowledgeDocDelete {
		t.Fatalf("delete: set=%v action=%s", set, ev.Action)
	}
	// 删完之后链上剩下的只是一个自增 id——名字必须**在删之前**读出来。
	if ev.ResourceName != "会被删掉的 SOP" {
		t.Fatalf("delete: resource name=%q", ev.ResourceName)
	}
	if ev.ResourceID != idStr(id) {
		t.Fatalf("delete: resource id=%q want %d", ev.ResourceID, id)
	}
	p := docPayload(t, ev)
	if got := mustKey(t, p, "path"); got != "网络" {
		t.Fatalf("delete: path=%v", got)
	}
	// 删文档不等于撤回 AI 已经按它给出的回答。
	if mustKey(t, p, "prior_answers_unchanged") != true {
		t.Fatalf("delete: prior_answers_unchanged=%v", p["prior_answers_unchanged"])
	}
}

// 失败不留行：只有成功才铸 token。上传一个不支持的类型，链上不能多出一行。
func TestAudit_KnowledgeDocUploadRejected(t *testing.T) {
	router, _ := newE2E(t)
	ct, body := buildUpload(t, "evil.exe", "MZ", "", "")
	_, _, set := auditReq(t, router, http.MethodPost, "/v1/knowledge/upload", ct, body)
	if set {
		t.Fatal("rejected upload must not mint a row")
	}
}
