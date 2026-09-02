// simple.go 简单周期（日/周/月）→ Cron 表达式转换（需求 §2.1.9：简单周期内部转 Cron 存储）。
package backup

import (
	"fmt"
	"strings"
)

// SimplePeriod 简单周期
type SimplePeriod struct {
	Unit   string `json:"unit"`   // day / week / month
	Every  int    `json:"every"`  // 每 N 个单位（>=1）
	Weekday int   `json:"weekday,omitempty"` // week 单位：1-7（周一=1）
	Hour   int    `json:"hour"`   // 0-23
	Minute int    `json:"minute"` // 0-59
}

// ToCron 转为标准 5 段 cron 表达式（分 时 日 月 周）
func (p SimplePeriod) ToCron() (string, error) {
	if p.Every <= 0 {
		p.Every = 1
	}
	if p.Hour < 0 || p.Hour > 23 || p.Minute < 0 || p.Minute > 59 {
		return "", fmt.Errorf("时间无效")
	}
	switch strings.ToLower(p.Unit) {
	case "day":
		if p.Every == 1 {
			return fmt.Sprintf("%d %d * * *", p.Minute, p.Hour), nil
		}
		return fmt.Sprintf("%d %d */%d * *", p.Minute, p.Hour, p.Every), nil
	case "week":
		if p.Weekday < 1 || p.Weekday > 7 {
			return "", fmt.Errorf("weekday 应为 1-7（周一=1）")
		}
		if p.Every != 1 {
			return "", fmt.Errorf("周周期暂不支持间隔（Every>1）")
		}
		return fmt.Sprintf("%d %d * * %d", p.Minute, p.Hour, p.Weekday), nil
	case "month":
		// 每月第 Every 天（简化：以 Every 为“几号”，1-28）
		if p.Every > 28 {
			return "", fmt.Errorf("月周期的日期应为 1-28")
		}
		return fmt.Sprintf("%d %d %d * *", p.Minute, p.Hour, p.Every), nil
	default:
		return "", fmt.Errorf("无效的周期单位: %s", p.Unit)
	}
}
