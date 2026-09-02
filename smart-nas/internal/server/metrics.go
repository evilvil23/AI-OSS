package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Metrics 轻量 Prometheus 文本协议指标收集器
//
// 说明：文档选用 prometheus client_golang，但当前离线环境不可用；
// 这里按 Prometheus 文本暴露格式（# HELP / # TYPE / sample）手写实现，
// 兼容 Prometheus / Grafana 抓取，接口简单可扩展。
type Metrics struct {
	mu       sync.Mutex
	gauges   map[string]metric
	counters map[string]metric
}

type metric struct {
	help  string
	value float64
}

// NewMetrics 创建指标收集器
func NewMetrics() *Metrics {
	return &Metrics{
		gauges:   make(map[string]metric),
		counters: make(map[string]metric),
	}
}

// SetGauge 设置仪表值（可升降）
func (m *Metrics) SetGauge(name, help string, v float64) {
	m.mu.Lock()
	m.gauges[name] = metric{help: help, value: v}
	m.mu.Unlock()
}

// IncCounter 计数器 +delta（默认 +1）
func (m *Metrics) IncCounter(name, help string, delta float64) {
	if delta == 0 {
		delta = 1
	}
	m.mu.Lock()
	c := m.counters[name]
	if c.help == "" {
		c.help = help
	}
	c.value += delta
	m.counters[name] = c
	m.mu.Unlock()
}

// Handler 输出 /metrics 文本
func (m *Metrics) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.String(http.StatusOK, m.render())
	}
}

// Snapshot 返回指标快照（JSON 友好结构，供前端监控页渲染）
func (m *Metrics) Snapshot() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	gauges := make(map[string]float64, len(m.gauges))
	for k, v := range m.gauges {
		gauges[k] = v.value
	}
	counters := make(map[string]float64, len(m.counters))
	for k, v := range m.counters {
		counters[k] = v.value
	}
	return map[string]interface{}{
		"gauges":   gauges,
		"counters": counters,
	}
}

func (m *Metrics) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, name := range m.sortedGauges() {
		g := m.gauges[name]
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, g.help, name, name, g.value)
	}
	for _, name := range m.sortedCounters() {
		c := m.counters[name]
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %g\n", name, c.help, name, name, c.value)
	}
	return b.String()
}

func (m *Metrics) sortedGauges() []string {
	names := make([]string, 0, len(m.gauges))
	for n := range m.gauges {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (m *Metrics) sortedCounters() []string {
	names := make([]string, 0, len(m.counters))
	for n := range m.counters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}