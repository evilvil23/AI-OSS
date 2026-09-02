// Package types 统一响应与消息类型。
package types

import "time"

// Response 统一 HTTP 响应格式
type Response struct {
	Code      int         `json:"code"`
	Message   string      `json:"message"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

// OK 构造成功响应
func OK(data interface{}) *Response {
	return &Response{Code: 0, Message: "success", Data: data, Timestamp: time.Now().Unix()}
}

// Fail 构造失败响应
func Fail(code int, message string) *Response {
	return &Response{Code: code, Message: message, Timestamp: time.Now().Unix()}
}

// 错误码规范（对应文档 §2.2.2）
const (
	CodeOK             = 0
	CodeUnauthorized   = 1001 // 未认证 / Token 过期
	CodeForbidden      = 1002 // 权限不足
	CodeFileNotFound   = 2001 // 文件不存在
	CodeNoStorageSpace = 2002 // 存储空间不足
	CodeModelNotLoaded = 3001 // AI 模型未加载
	CodeInferenceDim   = 3002 // AI 推理超时
	CodeDeviceOffline  = 4001 // IoT 设备离线
	CodeMihomeAPI      = 4002 // 米家 API 调用失败
	CodeBadRequest     = 4000 // 参数错误
	CodeServerError    = 5000 // 服务器内部错误
)

// WSMessage WebSocket 统一消息
type WSMessage struct {
	Type    string      `json:"type"`
	Data    interface{} `json:"data,omitempty"`
	Ts      int64       `json:"ts"`
}

// NewWSMessage 构造 WS 消息
func NewWSMessage(typ string, data interface{}) WSMessage {
	return WSMessage{Type: typ, Data: data, Ts: time.Now().Unix()}
}