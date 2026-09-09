//go:build windows

package ollama

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// spawnServe 后台拉起 ollama serve（Windows：隐藏窗口 + 独立进程组，随父进程退出不受影响）
func spawnServe(bin, bindHost string, numParallel int) (*exec.Cmd, error) {
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+bindHost)
	if numParallel > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("OLLAMA_NUM_PARALLEL=%d", numParallel))
	}
	// 注入 OLLAMA_MODELS：子进程继承的是本进程环境，User/Machine 级环境变量
	// 不会传播到已运行进程的子进程（如从终端 / 服务启动时），需主动读取并注入，
	// 否则 ollama serve 会退回默认模型目录导致模型 404
	if v := ollamaModelsEnv(); v != "" {
		cmd.Env = append(cmd.Env, "OLLAMA_MODELS="+v)
	}
	// HideWindow：不弹控制台窗口；CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS：
	// 脱离本服务控制台，服务退出后 Ollama 仍可运行（由 Shutdown 显式停止）
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// 释放句柄引用（进程独立运行，不需要 Wait）
	go func() { _ = cmd.Wait() }()
	return cmd, nil
}

// commonBinaryPaths Windows 常见安装位置
func commonBinaryPaths() []string {
	local := os.Getenv("LOCALAPPDATA")
	return []string{
		local + `\Programs\Ollama\ollama.exe`,
		`C:\Program Files\Ollama\ollama.exe`,
	}
}

// processAlive 判断进程是否存活
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}

// terminateProcess 停止进程（Windows 无 SIGTERM，直接 Kill；serve 无会话持久化需求）
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// terminateByPID 按 pid 停止（接管场景）
func terminateByPID(pid int) {
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
}
