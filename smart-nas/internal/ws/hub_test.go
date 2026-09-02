package ws

import "testing"

func TestDeviceType(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/126.0":        "pc",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1":  "pc",
		"Mozilla/5.0 (X11; Linux x86_64) Firefox/128.0":                 "pc",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)":        "mobile",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) Chrome/126.0 Mobile":  "mobile",
		"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) Safari/605.1":    "mobile",
		"Mozilla/5.0 (Linux; HarmonyOS; HUAWEI) Mobile Safari":          "mobile",
		"":                                                               "pc",
	}
	for ua, want := range cases {
		if got := DeviceType(ua); got != want {
			t.Errorf("DeviceType(%q) = %q, want %q", ua, got, want)
		}
	}
}

// TestOnlineUsersAggregatesDevices：同一用户的多个连接按设备类型去重聚合
func TestOnlineUsersAggregatesDevices(t *testing.T) {
	h := NewHub(nil)
	mk := func(id uint, name, role, device string) *Client {
		return &Client{userID: id, username: name, role: role, device: device}
	}
	h.register(mk(1, "alice", "master", "pc"))
	h.register(mk(1, "alice", "master", "mobile"))
	h.register(mk(1, "alice", "master", "pc")) // 同设备第三个连接（如多开标签页）
	h.register(mk(2, "bob", "user", "pc"))

	users := h.OnlineUsers()
	if len(users) != 2 {
		t.Fatalf("应聚合为 2 个用户, got %d: %+v", len(users), users)
	}
	var alice, bob *OnlineUser
	for i := range users {
		switch users[i].Username {
		case "alice":
			alice = &users[i]
		case "bob":
			bob = &users[i]
		}
	}
	if alice == nil || bob == nil {
		t.Fatalf("用户缺失: %+v", users)
	}
	if alice.Connections != 3 || bob.Connections != 1 {
		t.Fatalf("连接数不符: alice=%d bob=%d", alice.Connections, bob.Connections)
	}
	if len(alice.Devices) != 2 {
		t.Fatalf("alice 应有 2 种登录设备, got %v", alice.Devices)
	}
	hasPC, hasMobile := false, false
	for _, d := range alice.Devices {
		if d == "pc" {
			hasPC = true
		}
		if d == "mobile" {
			hasMobile = true
		}
	}
	if !hasPC || !hasMobile {
		t.Fatalf("alice 设备应含 pc 与 mobile, got %v", alice.Devices)
	}
	if len(bob.Devices) != 1 || bob.Devices[0] != "pc" {
		t.Fatalf("bob 设备应为 [pc], got %v", bob.Devices)
	}
}
