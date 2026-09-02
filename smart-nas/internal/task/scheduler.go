// Package task 定时任务调度与异步任务执行。
//
// 使用 robfig/cron/v3 实现定时任务（定时备份、回收站清理、AI 摘要），
// 并提供简单的 worker 队列执行耗时任务。
package task

import (
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"smart-nas/pkg/logger"
)

// jobInfo 定时任务注册信息
type jobInfo struct {
	id   cron.EntryID
	spec string
}

// Scheduler 定时任务调度器（包装 robfig/cron）
type Scheduler struct {
	cron *cron.Cron
	mu   sync.RWMutex
	jobs map[string]jobInfo
}

// NewScheduler 创建调度器；loc 可为空（默认本地时区）
func NewScheduler(loc *time.Location) *Scheduler {
	var c *cron.Cron
	if loc != nil {
		c = cron.New(cron.WithLocation(loc))
	} else {
		c = cron.New()
	}
	return &Scheduler{cron: c, jobs: make(map[string]jobInfo)}
}

// AddFunc 注册定时任务（spec 为标准 5 段 cron 表达式或 cron 预定义如 @daily）
func (s *Scheduler) AddFunc(spec, name string, fn func()) error {
	id, err := s.cron.AddFunc(spec, func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("定时任务 panic", "name", name, "panic", fmt.Sprint(r))
			}
		}()
		logger.Info("定时任务执行", "name", name)
		fn()
	})
	if err != nil {
		return fmt.Errorf("任务 %s 注册失败: %w", name, err)
	}
	s.mu.Lock()
	s.jobs[name] = jobInfo{id: id, spec: spec}
	s.mu.Unlock()
	return nil
}

// Remove 移除任务
func (s *Scheduler) Remove(name string) {
	s.mu.Lock()
	if info, ok := s.jobs[name]; ok {
		s.cron.Remove(info.id)
		delete(s.jobs, name)
	}
	s.mu.Unlock()
}

// Entries 任务快照列表（name + cron 表达式 + 下次执行时间）
func (s *Scheduler) Entries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Entry
	for name, info := range s.jobs {
		e := s.cron.Entry(info.id)
		out = append(out, Entry{Name: name, Spec: info.spec, Next: e.Next})
	}
	return out
}

// Start 启动调度器
func (s *Scheduler) Start() {
	s.cron.Start()
	logger.Info("定时任务调度器已启动", "count", len(s.jobs))
}

// Stop 停止调度器（等待当前任务结束）
func (s *Scheduler) Stop() {
	<-s.cron.Stop().Done()
	logger.Info("定时任务调度器已停止")
}