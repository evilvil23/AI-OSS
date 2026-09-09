//go:build !windows

package ollama

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// spawnServe 后台拉起 ollama serve（POSIX：setsid 独立会话，不随父进程退出）
// 生产环境建议改用 systemd 托管（[ai.ollama].managed = false）
func spawnServe(bin, bindHost string, numParallel int) (*exec.Cmd, error) {
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+bindHost)
	if numParallel > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("OLLAMA_NUM_PARALLEL=%d", numParallel))
	}
	if v := ollamaModelsEnv(); v != "" {
		cmd.Env = append(cmd.Env, "OLLAMA_MODELS="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd, nil
}

// commonBinaryPaths Linux 常见安装位置
func commonBinaryPaths() []string {
	return []string{
		"/usr/local/bin/ollama",
		"/usr/bin/ollama",
		"/opt/ollama/ollama",
	}
}

// processAlive 判断进程是否存活（signal 0 探测）
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// terminateProcess 优雅停止：SIGTERM → 超时 SIGKILL
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return cmd.Process.Kill()
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(10 * time.Second):
		return cmd.Process.Kill()
	}
}

// terminateByPID 按 pid 停止（接管场景）
func terminateByPID(pid int) {
	if pid <= 0 {
		return
	}
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		return
	}
	go func() {
		time.Sleep(10 * time.Second)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}()
}
