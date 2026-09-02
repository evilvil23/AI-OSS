// notify.go 邮件通知（SMTP）。
//
// 使用标准库 net/smtp（免依赖）。smtp_config JSON 结构：
// {"host":"smtp.example.com","port":587,"username":"user@example.com",
//  "password":"***","from":"nas@example.com","to":["admin@example.com"]}
package backup

import (
	"encoding/json"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPConfig SMTP 配置
type SMTPConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

// parseSMTP 解析任务内的 SMTP 配置 JSON
func parseSMTP(raw string) (*SMTPConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("SMTP 配置为空")
	}
	var c SMTPConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("SMTP 配置解析失败: %w", err)
	}
	if c.Host == "" || len(c.To) == 0 {
		return nil, fmt.Errorf("SMTP 配置缺少 host 或收件人")
	}
	if c.Port <= 0 {
		c.Port = 587
	}
	return &c, nil
}

// sendMail 发送纯文本邮件（不校验 TLS：家庭内网/中继场景，失败仅记日志不中断备份）
func sendMail(c *SMTPConfig, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	from := c.From
	if from == "" {
		from = c.Username
	}
	hdr := strings.Join([]string{
		"From: " + from,
		"To: " + strings.Join(c.To, ","),
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
	}, "\r\n")
	auth := smtp.PlainAuth("", c.Username, c.Password, c.Host)
	msg := []byte(hdr + body + "\r\n")
	return smtp.SendMail(addr, auth, from, c.To, msg)
}

// notifyResult 任务执行后按配置发送结果邮件（错误不外抛，仅日志）
func (s *Service) notifyResult(t *Task, h *History) {
	if !t.EnableEmail {
		return
	}
	cfg, err := parseSMTP(t.SMTPConfig)
	if err != nil {
		s.logf("任务 %d 邮件通知跳过: %v", t.ID, err)
		return
	}
	subject := fmt.Sprintf("[smart-nas] 备份任务「%s」%s", t.Name, statusText(h.Status))
	body := fmt.Sprintf(
		"任务: %s\n类型: %s\n状态: %s\n开始: %s\n结束: %s\n产物: %s\n大小: %d 字节\n备注: %s\n",
		t.Name, h.Type, statusText(h.Status),
		h.StartTime.Format("2006-01-02 15:04:05"),
		h.EndTime.Format("2006-01-02 15:04:05"),
		h.StorePath, h.TotalSize, h.Remark,
	)
	if err := sendMail(cfg, subject, body); err != nil {
		s.logf("任务 %d 邮件发送失败: %v", t.ID, err)
	}
}

func statusText(status string) string {
	switch status {
	case StatusSuccess:
		return "成功"
	case StatusPartial:
		return "部分成功（有文件被跳过）"
	case StatusFailed:
		return "失败"
	default:
		return status
	}
}
