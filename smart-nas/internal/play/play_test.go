package play

import (
	"os"
	"path/filepath"
	"testing"
)

// classifyVideo 播放模式决策用例
func TestClassifyVideo(t *testing.T) {
	cases := []struct {
		name      string
		ext       string
		height    int
		maxHeight int
		codec     string
		ffmpegOK  bool
		mode      string
		playable  bool
		tooLarge  bool
		reason    string
	}{
		{"mp4 1080p h264 直接播放", ".mp4", 1080, 1440, "h264", true, ModeDirect, true, false, ""},
		{"mp4 2K hevc 直接播放", ".mp4", 1440, 1440, "hevc", true, ModeDirect, true, false, ""},
		{"mp4 4K h264 允许直接流（高清提示）", ".mp4", 2160, 1440, "h264", true, ModeDirect, true, true, ""},
		{"mp4 8K 不转码直接流", ".mp4", 4320, 1440, "h264", true, ModeDirect, true, true, ""},
		{"mkv 1080p h264 转封装", ".mkv", 1080, 1440, "h264", true, ModeRemux, true, false, ""},
		{"mkv 1080p vp9 转封装", ".mkv", 1080, 1440, "vp9", true, ModeRemux, true, false, ""},
		{"mkv 1080p mpeg4 实时转码", ".mkv", 1080, 1440, "mpeg4", true, ModeTranscode, true, false, ""},
		{"mkv 4K h264 拒绝转封装（高清）", ".mkv", 2160, 1440, "h264", true, ModeRefuse, false, true, "too_large"},
		{"mkv 4K 转码也拒绝", ".mkv", 2160, 1440, "mpeg4", true, ModeRefuse, false, true, "too_large"},
		{"avi 720p h264 转封装", ".avi", 720, 1440, "h264", true, ModeRemux, true, false, ""},
		{"mkv h264 但无 ffmpeg 拒绝", ".mkv", 1080, 1440, "h264", false, ModeRefuse, false, false, "ffmpeg_missing"},
		{"pdf 不支持", ".pdf", 1080, 1440, "h264", true, ModeRefuse, false, false, "unsupported"},
		{"无扩展名不支持", "", 1080, 1440, "h264", true, ModeRefuse, false, false, "unsupported"},
		{"mp4 未知编码 → 转码", ".mp4", 720, 1440, "vc1", true, ModeTranscode, true, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, playable, tooLarge, reason := classifyVideo(c.ext, c.height, c.maxHeight, c.codec, c.ffmpegOK)
			if mode != c.mode || playable != c.playable || tooLarge != c.tooLarge || reason != c.reason {
				t.Fatalf("classifyVideo(%q,h=%d,max=%d,codec=%q,ff=%v) = (%q,%v,%v,%q), want (%q,%v,%v,%q)",
					c.ext, c.height, c.maxHeight, c.codec, c.ffmpegOK, mode, playable, tooLarge, reason,
					c.mode, c.playable, c.tooLarge, c.reason)
			}
		})
	}
}

// classifyVideo 边界：高度恰好等于上限允许（2K ≤ maxHeight 可播）
func TestClassifyVideoBoundary(t *testing.T) {
	mode, playable, _, _ := classifyVideo(".mkv", 1440, 1440, "h264", true)
	if mode != ModeRemux || !playable {
		t.Fatalf("1440 应视为 2K 以内可转封装，got mode=%s playable=%v", mode, playable)
	}
}

// parseProbeOutput 解析 ffprobe JSON 输出
func TestParseProbeOutput(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		want    probeResult
		wantErr bool
	}{
		{"带时长", `{"streams":[{"width":1920,"height":1080,"codec_name":"h264","duration":"65.567000"}]}`,
			probeResult{1920, 1080, "h264", 65.567}, false},
		{"无时长字段", `{"streams":[{"width":3840,"height":2160,"codec_name":"hevc"}]}`,
			probeResult{3840, 2160, "hevc", 0}, false},
		{"无视频流", `{"streams":[]}`, probeResult{}, true},
		{"非法 JSON", `not-json`, probeResult{}, true},
		{"格式错误的时长忽略", `{"streams":[{"width":640,"height":360,"codec_name":"h264","duration":"abc"}]}`,
			probeResult{640, 360, "h264", 0}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseProbeOutput([]byte(c.json))
			if c.wantErr {
				if err == nil {
					t.Fatal("期望解析失败，实际成功")
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got != c.want {
				t.Fatalf("parseProbeOutput = %+v, want %+v", got, c.want)
			}
		})
	}
}

// 音频扩展名识别：常见音频格式应被识别为可在线播放
func TestAudioExts(t *testing.T) {
	for _, ext := range []string{".mp3", ".flac", ".wav", ".aac", ".ogg", ".m4a", ".opus", ".wma"} {
		if !audioExts[ext] {
			t.Fatalf("音频扩展名 %s 应被识别", ext)
		}
	}
	if audioExts[".mp4"] || audioExts[".mkv"] {
		t.Fatal("视频扩展名不应被识别为音频")
	}
}

// 音频 MIME 类型映射
func TestAudioMime(t *testing.T) {
	cases := map[string]string{
		"a.mp3":  "audio/mpeg",
		"b.flac": "audio/flac",
		"c.wav":  "audio/wav",
		"d.aac":  "audio/aac",
		"e.ogg":  "audio/ogg",
		"f.m4a":  "audio/mp4",
		"g.opus": "audio/opus",
		"h.wma":  "audio/x-ms-wma",
		"i.xyz":  "audio/mpeg", // 未知扩展名回退
	}
	for name, want := range cases {
		if got := audioMime(name); got != want {
			t.Fatalf("audioMime(%q) = %q, want %q", name, got, want)
		}
	}
}

// resolveToolPath 内置二进制探测：绝对路径/含分隔符直接使用；裸名称优先命中
// 工作目录下的内置二进制，否则回退原值
func TestResolveToolPath(t *testing.T) {
	// 绝对路径直接使用
	abs := filepath.Join(t.TempDir(), "ffmpeg.exe")
	if got := resolveToolPath(abs); got != abs {
		t.Fatalf("绝对路径应原样返回: %q", got)
	}
	// 含目录分隔符直接使用
	if got := resolveToolPath("C:\\tools\\ffmpeg"); got != "C:\\tools\\ffmpeg" {
		t.Fatalf("含分隔符路径应原样返回: %q", got)
	}
	// 工作目录存在内置二进制 → 命中
	wd, _ := os.Getwd()
	oldWd := wd
	defer os.Chdir(oldWd)
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "ffprobe.exe")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveToolPath("ffprobe"); got != bin {
		t.Fatalf("应命中工作目录内置二进制: %q, want %q", got, bin)
	}
	// 无内置二进制 → 回退原值
	if got := resolveToolPath("ffmpeg"); got != "ffmpeg" {
		t.Fatalf("无内置二进制应回退原值: %q", got)
	}
}

// toolExists 可执行文件可用性判断
func TestToolExists(t *testing.T) {
	if toolExists("") {
		t.Fatal("空路径应不可用")
	}
	// 存在的文件
	f := filepath.Join(t.TempDir(), "tool.bin")
	if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !toolExists(f) {
		t.Fatal("存在的文件应可用")
	}
	// 不存在的文件
	if toolExists(filepath.Join(t.TempDir(), "nope.bin")) {
		t.Fatal("不存在的文件应不可用")
	}
	// 目录不是可执行文件
	if toolExists(t.TempDir()) {
		t.Fatal("目录不应视为可执行文件")
	}
}