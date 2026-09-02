// Package play 视频在线播放服务。
//
// 职责：
//   - 播放凭证（Playback Ticket）：创建 / 释放 / 校验，30 分钟无 Range 请求自动失效，
//     可选与请求 IP 绑定（防盗链）。
//   - 分辨率检测：调用 ffprobe 读取视频宽高 / 编码 / 时长，结果按 路径+大小+修改时间 缓存。
//   - 播放模式决策：mp4 直接 Range 流；mkv 等容器转封装（-c copy）为 MP4 后 Range 流；
//     浏览器不支持的编码降级为实时转码；超过 2K 不转码/转封装，直接提示下载。
package play

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"smart-nas/internal/config"
	"smart-nas/internal/storage"
	"smart-nas/internal/util"
	"smart-nas/pkg/logger"
)

// Ticket 播放凭证（与播放会话绑定）
type Ticket struct {
	Token      string
	FileID     uint
	UserID     uint
	IP         string
	CreatedAt  time.Time
	LastActive time.Time
}

// VideoInfo 视频信息（分辨率 / 编码 / 播放模式）
type VideoInfo struct {
	FileID    uint    `json:"file_id"`
	Name      string  `json:"name"`
	Size      int64   `json:"size"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Duration  float64 `json:"duration,omitempty"`
	Codec     string  `json:"codec"`
	Container string  `json:"container"`
	Playable  bool    `json:"playable"`   // 是否可在线播放（false 时前端仅显示下载）
	TooLarge  bool    `json:"too_large"`  // 分辨率超过 max_online_height
	Mode      string  `json:"mode"`       // direct / remux / transcode / refuse
	Reason    string  `json:"reason"`     // 不可播放原因（unsupported/probe_failed/too_large/ffmpeg_missing）
	IsAudio   bool    `json:"is_audio"`   // 音频文件（直接 Range 流，无视频流，无需 ffprobe）
}

// 播放模式常量
const (
	ModeDirect    = "direct"    // 直接 Range 流（mp4）
	ModeRemux     = "remux"     // 转封装 -c copy 为 mp4
	ModeTranscode = "transcode" // 实时转码（浏览器不支持原编码）
	ModeRefuse    = "refuse"    // 拒绝在线播放（仅提示下载）
)

// 支持在线播放的视频扩展名
var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".webm": true,
	".m4v": true, ".flv": true, ".ts": true, ".mpg": true, ".mpeg": true,
	".3gp": true, ".wmv": true, ".rmvb": true,
}

// 支持在线播放的音频扩展名（直接 Range 流，无需 ffprobe 视频探测）
var audioExts = map[string]bool{
	".mp3": true, ".flac": true, ".wav": true, ".aac": true,
	".ogg": true, ".m4a": true, ".opus": true, ".wma": true,
}

// 浏览器可直接播放的编码（转封装后无需转码）
var directPlayCodecs = map[string]bool{
	"h264": true, "hevc": true, "h265": true, "vp8": true, "vp9": true, "av1": true,
}

// probeResult ffprobe 探测结果
type probeResult struct {
	Width    int
	Height   int
	Codec    string
	Duration float64
}

// probeEntry 探测缓存条目
type probeEntry struct {
	info probeResult
}

// remuxTask 进行中的转封装任务（多请求共享等待）
type remuxTask struct {
	done chan struct{}
	err  error
}

// Service 视频播放服务
type Service struct {
	cfg     config.PlayConfig
	storage *storage.Service
	cacheDir string

	mu         sync.Mutex
	tickets    map[string]*Ticket
	probeCache map[string]probeEntry
	probeOrder []string // 简单 LRU 顺序（队首最旧）
	remuxTasks map[string]*remuxTask

	remuxSem chan struct{} // 转封装并发信号量
	transSem chan struct{} // 实时转码并发信号量

	done chan struct{}
}

// NewService 创建视频播放服务
func NewService(cfg config.PlayConfig, storageSvc *storage.Service) *Service {
	if cfg.MaxOnlineHeight <= 0 {
		cfg.MaxOnlineHeight = 1440
	}
	if cfg.RemuxConcurrency <= 0 {
		cfg.RemuxConcurrency = 3
	}
	if cfg.TranscodeConcurrency <= 0 {
		cfg.TranscodeConcurrency = 1
	}
	if cfg.TranscodeThreads <= 0 {
		cfg.TranscodeThreads = 2
	}
	if cfg.TicketTTLMinutes <= 0 {
		cfg.TicketTTLMinutes = 30
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.FFprobePath == "" {
		cfg.FFprobePath = "ffprobe"
	}
	// 内置二进制探测：优先使用可执行文件同目录 / 工作目录下的 ffmpeg/ffprobe，
	// 实现"随包分发、无需系统安装 FFmpeg"（v0.19）
	cfg.FFmpegPath = resolveToolPath(cfg.FFmpegPath)
	cfg.FFprobePath = resolveToolPath(cfg.FFprobePath)
	if cfg.CacheDir == "" {
		cfg.CacheDir = "./data/cache/video"
	}
	_ = os.MkdirAll(cfg.CacheDir, 0o755)
	s := &Service{
		cfg:        cfg,
		storage:    storageSvc,
		cacheDir:   cfg.CacheDir,
		tickets:    make(map[string]*Ticket),
		probeCache: make(map[string]probeEntry),
		remuxTasks: make(map[string]*remuxTask),
		remuxSem:   make(chan struct{}, cfg.RemuxConcurrency),
		transSem:   make(chan struct{}, cfg.TranscodeConcurrency),
		done:       make(chan struct{}),
	}
	go s.sweepLoop()
	return s
}

// Close 停止后台清理协程
func (s *Service) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// Enabled 播放功能是否启用
func (s *Service) Enabled() bool { return s.cfg.Enabled }

// ClearCache 清空转封装产物缓存目录与内存探测缓存（v0.21.3）。
// 正在写入的缓存文件（Windows 下被占用）会跳过并计数，全部成功返回 nil。
func (s *Service) ClearCache() error {
	s.mu.Lock()
	s.probeCache = make(map[string]probeEntry)
	s.probeOrder = nil
	s.mu.Unlock()
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	failed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(s.cacheDir, e.Name())); err != nil {
			failed++ // 多为正在播放被占用，尽力清理
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d 个缓存文件被占用，未能清除（正在播放时请稍后再试）", failed)
	}
	logger.Info("视频转封装缓存已清空", "dir", s.cacheDir)
	return nil
}

// ---- 指标 ----

// ActiveRemux 当前转封装任务数
func (s *Service) ActiveRemux() float64 { return float64(len(s.remuxSem)) }

// ActiveTranscode 当前实时转码任务数
func (s *Service) ActiveTranscode() float64 { return float64(len(s.transSem)) }

// ActiveTickets 当前有效播放凭证数
func (s *Service) ActiveTickets() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return float64(len(s.tickets))
}

// ---- 播放凭证 ----

// CreateTicket 创建播放凭证：校验文件存在、读权限与可播放性。
// 超过 2K 的非 mp4 文件（需转码/转封装）拒绝创建，直接提示下载。
func (s *Service) CreateTicket(userID, fileID uint, ip string) (*Ticket, VideoInfo, error) {
	f, err := s.storage.GetFileMeta(fileID)
	if err != nil {
		return nil, VideoInfo{}, errors.New("视频文件已移动或删除")
	}
	if !s.storage.CheckAccess(userID, fileID, false) {
		if reason := s.storage.DenyReason(userID, fileID, false); reason != "" {
			return nil, VideoInfo{}, errors.New(reason)
		}
		return nil, VideoInfo{}, errors.New("视频文件已移动或删除")
	}
	info, err := s.buildInfo(f)
	if err != nil {
		return nil, info, err
	}
	if info.Mode == ModeRefuse {
		return nil, info, errors.New(info.reasonText())
	}
	t := &Ticket{
		Token:      util.RandomToken(32),
		FileID:     fileID,
		UserID:     userID,
		IP:         ip,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	s.mu.Lock()
	s.tickets[t.Token] = t
	s.mu.Unlock()
	logger.Info("创建播放凭证", "file_id", fileID, "user_id", userID, "mode", info.Mode)
	return t, info, nil
}

// ReleaseTicket 释放播放凭证（关闭播放器时调用）
func (s *Service) ReleaseTicket(token string) {
	s.mu.Lock()
	delete(s.tickets, token)
	s.mu.Unlock()
}

// ValidateTicket 校验播放凭证：存在、未超时、IP 匹配（可选），并刷新最后活跃时间
func (s *Service) ValidateTicket(token, ip string) (*Ticket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[token]
	if !ok {
		return nil, false
	}
	ttl := time.Duration(s.cfg.TicketTTLMinutes) * time.Minute
	if time.Since(t.LastActive) > ttl {
		delete(s.tickets, token)
		return nil, false
	}
	if s.cfg.IPBind && ip != "" && t.IP != "" && ip != t.IP {
		return nil, false
	}
	t.LastActive = time.Now()
	return t, true
}

// sweepLoop 定期清理超时凭证
func (s *Service) sweepLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			ttl := time.Duration(s.cfg.TicketTTLMinutes) * time.Minute
			s.mu.Lock()
			for k, t := range s.tickets {
				if time.Since(t.LastActive) > ttl {
					delete(s.tickets, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

// ---- 视频信息 ----

// VideoInfo 查询视频信息（供文件列表渲染与播放前拦截）
func (s *Service) VideoInfo(userID, fileID uint) (VideoInfo, error) {
	f, err := s.storage.GetFileMeta(fileID)
	if err != nil {
		return VideoInfo{}, errors.New("视频文件已移动或删除")
	}
	if !s.storage.CheckAccess(userID, fileID, false) {
		if reason := s.storage.DenyReason(userID, fileID, false); reason != "" {
			return VideoInfo{}, errors.New(reason)
		}
		return VideoInfo{}, errors.New("视频文件已移动或删除")
	}
	return s.buildInfo(f)
}

// buildInfo 根据文件元数据 + ffprobe 探测结果决策播放模式
func (s *Service) buildInfo(f *storage.FileMeta) (VideoInfo, error) {
	info := VideoInfo{
		FileID:    f.ID,
		Name:      f.Name,
		Size:      f.Size,
		Container: strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Name), ".")),
	}
	ext := strings.ToLower(filepath.Ext(f.Name))
	// 音频文件：直接 Range 流（无需 ffprobe 视频探测）
	if audioExts[ext] {
		info.Playable = true
		info.Mode = ModeDirect
		info.IsAudio = true
		return info, nil
	}
	if !videoExts[ext] {
		info.Playable = false
		info.Mode = ModeRefuse
		info.Reason = "unsupported"
		return info, nil
	}
	path, err := s.storage.ResolveDownloadPath(f)
	if err != nil {
		info.Playable = false
		info.Mode = ModeRefuse
		info.Reason = "probe_failed"
		return info, nil
	}
	pr, err := s.probe(path)
	if err != nil {
		logger.Warn("视频探测失败", "file_id", f.ID, "path", path, "error", err)
		info.Playable = false
		info.Mode = ModeRefuse
		info.Reason = "probe_failed"
		return info, nil
	}
	info.Width = pr.Width
	info.Height = pr.Height
	info.Duration = pr.Duration
	info.Codec = pr.Codec

	info.Mode, info.Playable, info.TooLarge, info.Reason = classifyVideo(ext, pr.Height, s.cfg.MaxOnlineHeight, pr.Codec, s.ffmpegAvailable())
	return info, nil
}

// classifyVideo 播放模式决策（纯函数，便于单元测试）：
//   - 不支持格式 / 探测失败 → refuse
//   - > maxHeight（如 2K）：mp4 允许直接流（勉强可播，卡顿风险由前端提示）；其余拒绝（不转码/转封装）
//   - 2K 及以下：mp4 且浏览器可播编码 → direct；其余容器可播编码 → remux；否则 → transcode
func classifyVideo(ext string, height int, maxHeight int, codec string, ffmpegOK bool) (mode string, playable bool, tooLarge bool, reason string) {
	if !videoExts[ext] {
		return ModeRefuse, false, false, "unsupported"
	}
	if height > maxHeight {
		if ext == ".mp4" {
			return ModeDirect, true, true, ""
		}
		return ModeRefuse, false, true, "too_large"
	}
	if !ffmpegOK {
		return ModeRefuse, false, false, "ffmpeg_missing"
	}
	if directPlayCodecs[codec] {
		if ext == ".mp4" {
			return ModeDirect, true, false, ""
		}
		return ModeRemux, true, false, ""
	}
	return ModeTranscode, true, false, ""
}

// reasonText 不可播放原因的中文提示
func (v VideoInfo) reasonText() string {
	switch v.Reason {
	case "unsupported":
		return "该格式暂不支持在线播放，请下载"
	case "probe_failed":
		return "无法识别视频分辨率，暂不支持在线播放，请下载"
	case "too_large":
		return "该视频分辨率超过2K，为获得流畅体验，建议下载后本地播放"
	case "ffmpeg_missing":
		return "服务器未安装 ffmpeg，无法在线播放该格式，请下载"
	default:
		return "该视频暂不支持在线播放，请下载"
	}
}

// ffmpegAvailable ffmpeg / ffprobe 是否可用（实际校验二进制存在，避免误判）
func (s *Service) ffmpegAvailable() bool {
	return toolExists(s.cfg.FFmpegPath) && toolExists(s.cfg.FFprobePath)
}

// resolveToolPath 解析 ffmpeg/ffprobe 可执行文件路径：
//   - 配置为绝对路径或含目录分隔符 → 视为用户显式指定，直接使用；
//   - 否则优先查找可执行文件同目录下的内置二进制（打包集成，无需系统安装），
//     其次工作目录，最后回退到配置值（由 PATH 查找）。
func resolveToolPath(configured string) string {
	if configured == "" {
		return ""
	}
	if filepath.IsAbs(configured) || strings.ContainsAny(configured, `/\`) {
		return configured
	}
	// 内置二进制：可执行文件同目录（Windows 下自动尝试 .exe 后缀）
	dirs := []string{}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	for _, dir := range dirs {
		for _, name := range []string{configured, configured + ".exe"} {
			c := filepath.Join(dir, name)
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
				return c
			}
		}
	}
	return configured
}

// toolExists 判断可执行文件是否真实可用：绝对/相对路径检查文件存在，
// 裸名称（如 ffmpeg）通过 PATH 查找。
func toolExists(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) || strings.ContainsAny(p, `/\`) {
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	_, err := exec.LookPath(p)
	return err == nil
}

// ---- 探测缓存 ----

// probe 探测视频信息（带缓存，key = 路径+大小+修改时间）
func (s *Service) probe(path string) (probeResult, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return probeResult{}, err
	}
	key := fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano())
	s.mu.Lock()
	if e, ok := s.probeCache[key]; ok {
		s.mu.Unlock()
		return e.info, nil
	}
	s.mu.Unlock()

	pr, err := runProbe(s.cfg.FFprobePath, path)
	if err != nil {
		return probeResult{}, err
	}
	s.mu.Lock()
	s.probeCache[key] = probeEntry{info: pr}
	s.probeOrder = append(s.probeOrder, key)
	// 简单 LRU：超过 1024 条时淘汰最旧
	for len(s.probeOrder) > 1024 {
		old := s.probeOrder[0]
		s.probeOrder = s.probeOrder[1:]
		delete(s.probeCache, old)
	}
	s.mu.Unlock()
	return pr, nil
}
