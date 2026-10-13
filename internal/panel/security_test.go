package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestPanel() *Panel {
	// 启用鉴权：未带 key 的请求一律 401，不进入依赖 Pool/Upstream 的 handler。
	return New(Config{Version: "test", APIKey: "test-key"})
}

// 面板安全响应头必须覆盖：页面、静态脚本、鉴权失败响应。
func TestSecurityHeadersOnAllPanelResponses(t *testing.T) {
	p := newTestPanel()
	paths := []struct{ method, path string }{
		{"GET", "/panel/"},
		{"GET", "/panel/app.js"},
		{"GET", "/panel/api/overview"}, // 401（未提供 key）
		{"POST", "/panel/api/config"},  // 401
		{"GET", "/panel/api/nonexistent"},
	}
	for _, c := range paths {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		h := rec.Header()
		if got := h.Get("Content-Security-Policy"); got == "" {
			t.Errorf("%s %s: missing CSP", c.method, c.path)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s %s: X-Content-Type-Options=%q", c.method, c.path, h.Get("X-Content-Type-Options"))
		}
		if h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s %s: X-Frame-Options=%q", c.method, c.path, h.Get("X-Frame-Options"))
		}
		if h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s %s: Referrer-Policy=%q", c.method, c.path, h.Get("Referrer-Policy"))
		}
	}
}

// CSP 必须禁止内联脚本与 iframe 嵌套（严格策略的核心约束）。
func TestCSPDisallowsInlineScriptAndFraming(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	csp := rec.Header().Get("Content-Security-Policy")

	for _, must := range []string{
		"script-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"default-src 'none'",
	} {
		if !strings.Contains(csp, must) {
			t.Errorf("CSP missing %q; got: %s", must, csp)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "script-src 'unsafe-inline'") {
		t.Errorf("CSP must not allow unsafe-inline scripts; got: %s", csp)
	}
}

// 页面必须引用外部脚本（内联脚本会被上面的 CSP 拦掉，页面将完全不可用）。
func TestIndexReferencesExternalScript(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `<script src="app.js"></script>`) {
		t.Error("index.html must load app.js externally (inline script is blocked by CSP)")
	}
	// 反例保护：出现内联 <script>...</script> 内容块即为回归
	if strings.Contains(body, "<script>\n") || strings.Contains(body, "<script> ") {
		t.Error("index.html still contains an inline <script> block; CSP would block it")
	}
}

// app.js 必须能作为同源脚本取到且类型正确（否则页面白屏）。
func TestAppScriptServed(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type=%q want javascript", ct)
	}
	if !strings.Contains(rec.Body.String(), "'use strict'") {
		t.Error("app.js body looks wrong")
	}
}

// UID 白名单：拒绝路径穿越与异常字符，放行真实 UUID 形态。
func TestValidUID(t *testing.T) {
	ok := []string{
		"248890d9-bb26-4131-87a7-4ec74d472344",
		"abc_123-XYZ",
		"a",
	}
	bad := []string{
		"",
		"../../evil",
		"x/../../y",
		`..\..\evil`,
		"a/b",
		"a\\b",
		"uid with space",
		"uid\nnewline",
		"uid\x00null",
		"café",
		strings.Repeat("a", 65), // 超长
	}
	for _, u := range ok {
		if !validUID(u) {
			t.Errorf("validUID(%q) = false, want true", u)
		}
	}
	for _, u := range bad {
		if validUID(u) {
			t.Errorf("validUID(%q) = true, want false", u)
		}
	}
}

// 未带密钥的 API 请求必须 401；携带正确密钥则通过鉴权层（不再是 401）。
func TestAuthLayerBehavior(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/overview", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key: code=%d want 401", rec.Code)
	}
	// 用不存在的路由验证"带正确 key 已过鉴权"（避免触碰依赖 nil 的 handler）。
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/panel/api/nonexistent", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	p.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusUnauthorized {
		t.Error("valid key must pass the auth layer")
	}
}

// 内嵌静态资源必须带 ETag + no-cache，且支持条件请求 304：
// 否则浏览器启发式缓存会在升级二进制后继续复用旧 app.js（真踩过：
// 前端改了却看不到效果，得硬刷新），on-call 排查代价很高。
func TestEmbeddedAssetsCacheable(t *testing.T) {
	p := newTestPanel()
	for _, path := range []string{"/panel/", "/panel/app.js"} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d want 200", path, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control=%q want %q", path, cc, "no-cache")
		}
		etag := rec.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: missing ETag", path)
		}

		// 带同一 ETag 再来一次：必须 304 且无响应体（省掉整份资源重传）。
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("If-None-Match", etag)
		rec2 := httptest.NewRecorder()
		p.ServeHTTP(rec2, req)
		if rec2.Code != http.StatusNotModified {
			t.Errorf("%s: conditional GET code=%d want 304", path, rec2.Code)
		}
		if rec2.Body.Len() != 0 {
			t.Errorf("%s: 304 must have empty body, got %d bytes", path, rec2.Body.Len())
		}

		// 过期 ETag 必须回 200 全量（否则升级后永远拿不到新版）。
		req = httptest.NewRequest("GET", path, nil)
		req.Header.Set("If-None-Match", `"stale-etag-from-old-binary"`)
		rec3 := httptest.NewRecorder()
		p.ServeHTTP(rec3, req)
		if rec3.Code != http.StatusOK {
			t.Errorf("%s: mismatched ETag code=%d want 200", path, rec3.Code)
		}
		if rec3.Body.Len() == 0 {
			t.Errorf("%s: mismatched ETag must resend full body", path)
		}
	}
}

// ETag 必须由内容决定（内容不同 => ETag 不同），否则校验形同虚设。
func TestETagDerivedFromContent(t *testing.T) {
	if etagOf([]byte("a")) == etagOf([]byte("b")) {
		t.Error("etagOf must vary with content")
	}
	if etagOf([]byte("same")) != etagOf([]byte("same")) {
		t.Error("etagOf must be deterministic")
	}
	if !strings.HasPrefix(etagOf(appJS), `"`) || !strings.HasSuffix(etagOf(appJS), `"`) {
		t.Error("etagOf must return a quoted ETag")
	}
}
