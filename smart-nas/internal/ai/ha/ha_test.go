// ha_test.go HomeAssistant 客户端与 AI 工具链单元测试（Mock 响应验证）。
package ha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newMockHA 启动 Mock HA 服务：校验 token，返回固定实体数据
func newMockHA(t *testing.T, captured *map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if captured != nil {
			(*captured)["auth"] = auth
		}
		if auth != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Unauthorized"}`))
			return
		}
		switch {
		case r.URL.Path == "/api/":
			_, _ = w.Write([]byte(`{"message":"API running."}`))
		case r.URL.Path == "/api/states":
			_, _ = w.Write([]byte(`[
				{"entity_id":"light.living_room","state":"on","attributes":{"friendly_name":"客厅灯","brightness":128}},
				{"entity_id":"switch.fan","state":"off","attributes":{"friendly_name":"风扇"}},
				{"entity_id":"sensor.temp","state":"26.5","attributes":{"friendly_name":"温度","unit_of_measurement":"°C"}}
			]`))
		case r.URL.Path == "/api/states/light.living_room":
			_, _ = w.Write([]byte(`{"entity_id":"light.living_room","state":"on","attributes":{"friendly_name":"客厅灯","brightness":128}}`))
		case strings.HasPrefix(r.URL.Path, "/api/services/light/") && r.Method == http.MethodPost:
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if captured != nil {
				b, _ := json.Marshal(body)
				(*captured)["service_body"] = string(b)
			}
			_, _ = w.Write([]byte(`[{"entity_id":"light.living_room","state":"on"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return httptest.NewServer(mux)
}

func TestCheckConnection(t *testing.T) {
	srv := newMockHA(t, nil)
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	if err := c.CheckConnection(context.Background()); err != nil {
		t.Fatalf("连通性自检失败: %v", err)
	}
	// 错误 token 应报认证失败
	bad := NewClient(srv.URL, "wrong-token")
	if err := bad.CheckConnection(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误 token 应返回 401 错误，实际: %v", err)
	}
}

func TestListStatesWithDomainFilter(t *testing.T) {
	srv := newMockHA(t, nil)
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	ctx := context.Background()

	all, err := c.ListStates(ctx, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("全量列表失败: n=%d err=%v", len(all), err)
	}
	lights, err := c.ListStates(ctx, "light")
	if err != nil || len(lights) != 1 || lights[0].EntityID != "light.living_room" {
		t.Fatalf("light 过滤失败: %+v err=%v", lights, err)
	}
}

func TestCallServiceSendsEntityID(t *testing.T) {
	captured := map[string]string{}
	srv := newMockHA(t, &captured)
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	if err := c.CallService(context.Background(), "light", "turn_on", "light.living_room",
		map[string]interface{}{"brightness": 200}); err != nil {
		t.Fatalf("服务调用失败: %v", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(captured["service_body"]), &body); err != nil {
		t.Fatal(err)
	}
	if body["entity_id"] != "light.living_room" || body["brightness"] != float64(200) {
		t.Fatalf("服务请求体不符: %v", body)
	}
}

func TestAIToolsChain(t *testing.T) {
	srv := newMockHA(t, nil)
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")
	tools := Tools(c)
	if len(tools) != 3 {
		t.Fatalf("应注册 3 个工具，实际 %d", len(tools))
	}
	ctx := context.Background()

	// 工具 1：列表
	t1 := tools[0]
	if t1.Name() != "ha_list_devices" {
		t.Fatalf("工具名不符: %s", t1.Name())
	}
	out, err := t1.Execute(ctx, json.RawMessage(`{"domain":"sensor"}`))
	if err != nil || !strings.Contains(out, "sensor.temp") {
		t.Fatalf("ha_list_devices 失败: out=%s err=%v", out, err)
	}

	// 工具 2：状态
	t2 := tools[1]
	out, err = t2.Execute(ctx, json.RawMessage(`{"entity_id":"light.living_room"}`))
	if err != nil || !strings.Contains(out, `"state":"on"`) {
		t.Fatalf("ha_get_state 失败: out=%s err=%v", out, err)
	}
	if _, err = t2.Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("缺少 entity_id 应报错")
	}

	// 工具 3：控制（含 domain 自动纠正）
	t3 := tools[2]
	if t3.Name() != "ha_call_service" {
		t.Fatalf("工具名不符: %s", t3.Name())
	}
	out, err = t3.Execute(ctx, json.RawMessage(`{"domain":"wrong","service":"turn_off","entity_id":"light.living_room"}`))
	if err != nil || !strings.Contains(out, `"result":"ok"`) {
		t.Fatalf("ha_call_service 失败: out=%s err=%v", out, err)
	}

	// 未配置降级：nil 客户端返回空工具列表
	if got := Tools(nil); got != nil {
		t.Fatal("未配置时 Tools(nil) 应返回 nil（降级提示由无工具承担）")
	}
}
