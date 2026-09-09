// web_test.go 前端 template/static 重组后的链路测试（v0.22）。
//
// 验证：模板语法合法、WebVersion 注入、SPA 回退渲染、API 未命中 JSON 404、
// 静态资源经 /static 服务。模板与静态目录使用仓库真实文件（../../web/）。
package server

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// loadRealWebTemplate 加载仓库真实页面模板（测试运行目录为 internal/server/）
func loadRealWebTemplate(t *testing.T) *template.Template {
	t.Helper()
	tpl, err := template.ParseFiles("../../web/templates/index.html")
	if err != nil {
		t.Fatalf("解析前端页面模板失败: %v", err)
	}
	return tpl
}

// newWebTestServer 构造带前端模板的最小 Server（不依赖数据库等重量依赖）
func newWebTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &Server{engine: gin.New(), webTmpl: loadRealWebTemplate(t)}
	s.engine.NoRoute(s.staticFallback())
	return s
}

// TestWebTemplateSyntax 模板必须可解析且包含 WebVersion 占位与 /static 引用
func TestWebTemplateSyntax(t *testing.T) {
	data, err := template.ParseFiles("../../web/templates/index.html")
	if err != nil {
		t.Fatalf("模板语法错误: %v", err)
	}
	_ = data
	tplBytes := loadRealWebTemplate(t).Tree.Root.String()
	if !strings.Contains(tplBytes, "/static/css/style.css") {
		t.Fatal("模板应引用 /static/css/style.css")
	}
	if !strings.Contains(tplBytes, "/static/js/app.js") {
		t.Fatal("模板应引用 /static/js/app.js")
	}
}

// TestWebRenderInjectsVersion / 与 SPA 回退渲染模板并注入 WebVersion
func TestWebRenderInjectsVersion(t *testing.T) {
	s := newWebTestServer(t)
	for _, path := range []string{"/", "/files", "/login"} {
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s 预期 200，实际 %d", path, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "/static/js/app.js?v="+WebVersion) {
			t.Fatalf("GET %s 未注入 WebVersion（%s）", path, WebVersion)
		}
		if strings.Contains(body, "{{") {
			t.Fatalf("GET %s 输出残留模板占位符", path)
		}
	}
}

// TestStaticFallbackAPINotFound API / WebSocket / /static 未命中返回 JSON 404（不回退前端）
func TestStaticFallbackAPINotFound(t *testing.T) {
	s := newWebTestServer(t)
	for _, path := range []string{"/api/nope", "/api/files/x/sub", "/ws", "/static/js/missing.js", "/metrics/x", "/files/upload/x", "/dav/x"} {
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("GET %s 预期 404，实际 %d", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), `"code"`) {
			t.Fatalf("GET %s 预期 JSON 404 响应体", path)
		}
	}
}

// TestStaticFileServed /static 真实文件服务（gin Static）
func TestStaticFileServed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Static("/static", "../../web/static")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/static/js/app.js", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /static/js/app.js 预期 200，实际 %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("Content-Type 不符: %q", w.Header().Get("Content-Type"))
	}
}

// TestStaticFallbackNoTemplate 模板缺失（webTmpl=nil）时回退 JSON 404，不 panic
func TestStaticFallbackNoTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{engine: gin.New()} // webTmpl 未加载
	s.engine.NoRoute(s.staticFallback())
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("模板缺失时预期 404，实际 %d", w.Code)
	}
}
