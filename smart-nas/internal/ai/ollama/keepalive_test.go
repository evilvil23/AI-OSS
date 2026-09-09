package ollama

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestKeepAliveMarshal 验证 keep_alive 序列化：纯数字输出为 JSON number，
// duration 字符串保持字符串，避免 Ollama 报 "time: missing unit"。
func TestKeepAliveMarshal(t *testing.T) {
	cases := []struct {
		name string
		in   KeepAlive
		want string
	}{
		{"空值", "", "null"},
		{"常驻-1", "-1", "-1"},           // 裸数字 → JSON number
		{"卸载0", "0", "0"},             // 裸数字 → JSON number
		{"duration 5m", "5m", `"5m"`},   // duration → JSON string
		{"duration 1h", "1h", `"1h"`},   // duration → JSON string
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.in)
			if err != nil {
				t.Fatalf("Marshal 失败: %v", err)
			}
			if string(b) != c.want {
				t.Fatalf("期望 %s，实际 %s", c.want, string(b))
			}
		})
	}
}

// TestGenerateRequestKeepAlive 验证 Warmup/UnloadModel 请求体 keep_alive 字段形态：
// "-1" 应为 JSON number（预热常驻），"0" 应为 JSON number（卸载）。
func TestGenerateRequestKeepAlive(t *testing.T) {
	cases := []struct {
		name       string
		req        GenerateRequest
		wantNum    bool   // keep_alive 是否为 JSON number（true）还是字符串（false）
		wantVal    string // 期望的 JSON 值（不含引号时比较数字形态）
		wantAbsent bool   // keep_alive 是否应缺失
	}{
		{"预热常驻", GenerateRequest{Model: "m", Prompt: "", KeepAlive: "-1"}, true, "-1", false},
		{"卸载", GenerateRequest{Model: "m", Prompt: "", KeepAlive: "0"}, true, "0", false},
		{"duration", GenerateRequest{Model: "m", Prompt: "", KeepAlive: "5m"}, false, "5m", false},
		{"缺省", GenerateRequest{Model: "m", Prompt: ""}, false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.req)
			if err != nil {
				t.Fatalf("Marshal 失败: %v", err)
			}
			s := string(b)
			if c.wantAbsent {
				if strings.Contains(s, "keep_alive") {
					t.Fatalf("不应包含 keep_alive，实际 %s", s)
				}
				return
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatalf("Unmarshal 失败: %v", err)
			}
			ka, ok := raw["keep_alive"]
			if !ok {
				t.Fatalf("缺少 keep_alive，实际 %s", s)
			}
			trimmed := strings.TrimSpace(string(ka))
			if c.wantNum {
				// 期望为裸数字（无引号）
				if strings.HasPrefix(trimmed, `"`) {
					t.Fatalf("keep_alive 应为 JSON number，实际为字符串 %s", trimmed)
				}
				if trimmed != c.wantVal {
					t.Fatalf("期望 %s，实际 %s", c.wantVal, trimmed)
				}
			} else {
				if !strings.HasPrefix(trimmed, `"`) || strings.Trim(trimmed, `"`) != c.wantVal {
					t.Fatalf("期望字符串 %q，实际 %s", c.wantVal, trimmed)
				}
			}
		})
	}
}
