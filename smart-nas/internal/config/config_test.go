package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 原文件样例：含独立注释行、空行、行内注释与字符串转义
const sampleOrig = `# ===== 服务器配置 =====
[server]
# 服务监听端口
port = 8080
mode = 'release'  # 运行模式

# ===== AI 配置 =====
[ai]
# 是否启用
enabled = false
ollama_host = 'http://localhost:11434'

[ai.rag]
# 是否启用知识库
enabled = true
`

// TestMergeCommentsPreservesComments 值被替换，注释 / 空行 / 行内注释全部保留
func TestMergeCommentsPreservesComments(t *testing.T) {
	marshaled := `[server]
port = 9090
mode = 'debug'

[ai]
enabled = true
ollama_host = 'http://127.0.0.1:11434'

[ai.rag]
enabled = false
chunk_size = 512
`
	got := mergeComments(sampleOrig, marshaled)
	want := `# ===== 服务器配置 =====
[server]
# 服务监听端口
port = 9090
mode = 'debug'  # 运行模式

# ===== AI 配置 =====
[ai]
# 是否启用
enabled = true
ollama_host = 'http://127.0.0.1:11434'

[ai.rag]
# 是否启用知识库
enabled = false
chunk_size = 512
`
	if got != want {
		t.Errorf("合并结果不符\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestMergeCommentsAppendsNewSection 原文件缺失的分段追加到文件尾（带表头）
func TestMergeCommentsAppendsNewSection(t *testing.T) {
	orig := "# 说明\n[server]\nport = 8080\n"
	marshaled := "[server]\nport = 8080\n\n[ai.deploy]\nmode = 'auto'\n"
	got := mergeComments(orig, marshaled)
	for _, want := range []string{"# 说明", "[server]", "port = 8080", "[ai.deploy]", "mode = 'auto'"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q\n%s", want, got)
		}
	}
	if strings.Index(got, "[ai.deploy]") < strings.Index(got, "port = 8080") {
		t.Errorf("新分段应追加在文件尾\n%s", got)
	}
}

// TestMergeCommentsKeepsUnknownKeys 原文件中的未知键不被删除
func TestMergeCommentsKeepsUnknownKeys(t *testing.T) {
	orig := "[server]\nport = 8080\n# 用户自定义项\ncustom_flag = true\n"
	marshaled := "[server]\nport = 9090\n"
	got := mergeComments(orig, marshaled)
	for _, want := range []string{"port = 9090", "# 用户自定义项", "custom_flag = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q\n%s", want, got)
		}
	}
}

// TestMergeCommentsQuotedValues 值中的 = 与 # 不被误判为分隔符 / 注释
func TestMergeCommentsQuotedValues(t *testing.T) {
	orig := `[auth]
jwt_secret = 'old'
note = "he said \"a=b#c\""  # 行内注释
`
	marshaled := `[auth]
jwt_secret = 'new'
note = "he said \"a=b#c\""
`
	got := mergeComments(orig, marshaled)
	for _, want := range []string{"jwt_secret = 'new'", `note = "he said \"a=b#c\""  # 行内注释`} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q\n%s", want, got)
		}
	}
}

// TestSplitTOMLKV 键值行解析
func TestSplitTOMLKV(t *testing.T) {
	cases := []struct {
		line     string
		key, val string
		comment  string
		ok       bool
	}{
		{"port = 8080", "port", "8080", "", true},
		{"  mode = 'release'  # 运行模式", "mode", "'release'", "# 运行模式", true},
		{"url = 'http://a/b#c'", "url", "'http://a/b#c'", "", true},
		{`path = "C:\\x#y"`, "path", `"C:\\x#y"`, "", true},
		{"# 纯注释", "", "", "", false},
		{"[ai.rag]", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, c := range cases {
		key, val, comment, ok := splitTOMLKV(c.line)
		if ok != c.ok || key != c.key || val != c.val || comment != c.comment {
			t.Errorf("splitTOMLKV(%q) = (%q,%q,%q,%v)，期望 (%q,%q,%q,%v)",
				c.line, key, val, comment, ok, c.key, c.val, c.comment, c.ok)
		}
	}
}

// TestRestartRequiredFlag 待重启标志：仅启动期装配项变更才置位
func TestRestartRequiredFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[ai]\nenabled = false\ntemperature = 0.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.RestartRequired() {
		t.Fatal("初始状态不应待重启")
	}
	// 非启动期装配项（温度热更新即时生效）不应置位
	if err := m.UpdateConfig(map[string]interface{}{
		"ai": map[string]interface{}{"temperature": 0.9},
	}); err != nil {
		t.Fatal(err)
	}
	if m.RestartRequired() {
		t.Error("仅修改 temperature 不应置位待重启")
	}
	// 启动期装配项（AI 总开关）变更应置位
	if err := m.UpdateConfig(map[string]interface{}{
		"ai": map[string]interface{}{"enabled": true},
	}); err != nil {
		t.Fatal(err)
	}
	if !m.RestartRequired() {
		t.Error("修改 ai.enabled 应置位待重启")
	}
}

// TestUpdateConfigPreservesFileComments 端到端：设置更新落盘后注释仍在
func TestUpdateConfigPreservesFileComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(sampleOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateConfig(map[string]interface{}{
		"server": map[string]interface{}{"port": 9090},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# ===== 服务器配置 =====", "# 服务监听端口", "port = 9090", "# 运行模式"} {
		if !strings.Contains(text, want) {
			t.Errorf("落盘后缺少 %q\n%s", want, text)
		}
	}
	if !strings.Contains(text, "port = 9090") {
		t.Errorf("新值未落盘\n%s", text)
	}
}
