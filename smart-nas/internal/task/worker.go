package task

import (
	"context"
	"sync"
	"time"

	"smart-nas/pkg/logger"
)

// Entry 定时任务快照
type Entry struct {
	Name string    `json:"name"`
	Spec string    `json:"spec"`
	Next time.Time `json:"next"`
}

// JobFunc 异步任务函数
type JobFunc func(ctx context.Context) error

// Job 一个待执行的异步任务
type Job struct {
	Name string
	Fn   JobFunc
}

// Worker 队列式异步任务执行器（固定并发数）
type Worker struct {
	queue  chan Job
	size   int
	wg     sync.WaitGroup
	cancel context.CancelFunc
	ctx    context.Context
}

// NewWorker 创建并发数为 size 的 Worker
func NewWorker(size int) *Worker {
	if size <= 0 {
		size = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{queue: make(chan Job, 256), size: size, cancel: cancel, ctx: ctx}
}

// Start 启动 worker 协程
func (w *Worker) Start() *Worker {
	for i := 0; i < w.size; i++ {
		w.wg.Add(1)
		go w.run()
	}
	logger.Info("异步任务 Worker 已启动", "size", w.size)
	return w
}

// Submit 提交任务（非阻塞，队列满时阻塞）
func (w *Worker) Submit(name string, fn JobFunc) {
	select {
	case <-w.ctx.Done():
		return
	case w.queue <- Job{Name: name, Fn: fn}:
	}
}

func (w *Worker) run() {
	defer w.wg.Done()
	for {
		select {
		case <-w.ctx.Done():
			return
		case job := <-w.queue:
			start := time.Now()
			if err := job.Fn(w.ctx); err != nil {
				logger.Error("异步任务失败", "name", job.Name, "error", err, "elapsed", time.Since(start))
			} else {
				logger.Info("异步任务完成", "name", job.Name, "elapsed", time.Since(start))
			}
		}
	}
}

// Stop 优雅停止：取消上下文并等待已提交任务结束
func (w *Worker) Stop() {
	w.cancel()
	w.wg.Wait()
}