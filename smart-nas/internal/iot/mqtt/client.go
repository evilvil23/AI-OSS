// Package mqtt 精简 MQTT 3.1.1 客户端。
//
// 说明：文档选用 paho.mqtt.golang，但当前离线环境不可用；
// 这里自实现最小可用客户端（CONNECT/SUBSCRIBE/PUBLISH Qos0/PINGREQ/
// DISCONNECT），接口保持常见客户端风格，后续可无缝替换为 paho 等成熟库。
package mqtt

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 消息类型
const (
	msgCONNECT    byte = 1
	msgCONNACK    byte = 2
	msgPUBLISH    byte = 3
	msgSUBSCRIBE  byte = 8
	msgSUBACK     byte = 9
	msgPINGREQ    byte = 12
	msgPINGRESP   byte = 13
	msgDISCONNECT byte = 14
)

// OnMessageHandler 收到订阅消息时的回调
type OnMessageHandler func(topic string, payload []byte)

// Client MQTT 客户端
type Client struct {
	mu        sync.Mutex
	conn      net.Conn
	br        *bufio.Reader
	clientID  string
	keepalive int
	connected atomic.Bool
	OnMessage OnMessageHandler
	cancel    context.CancelFunc
	done      chan struct{}
}

// Dial 解析 broker 地址（支持 tcp://host:port 或 host:port）
func Dial(broker string) (*Client, error) {
	host, port := brokerHostPort(broker)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接 MQTT broker 失败: %w", err)
	}
	c := &Client{
		conn:      conn,
		br:        bufio.NewReader(conn),
		keepalive: 60,
		done:      make(chan struct{}),
	}
	return c, nil
}

// Connect 发送 CONNECT 并等待 CONNACK；clientID 非空时使用
func (c *Client) Connect(ctx context.Context, clientID, username, password string, keepalive int) error {
	if clientID == "" {
		clientID = "smart-nas-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if keepalive > 0 {
		c.keepalive = keepalive
	}
	c.clientID = clientID

	var flags byte
	hasUser := username != ""
	hasPass := password != ""
	if hasUser {
		flags |= 0x40
	}
	if hasPass {
		flags |= 0x80
	}
	payload := append(mqttString("MQTT"), 4, flags)
	payload = append(payload, uint16Bytes(uint16(c.keepalive))...)
	payload = append(payload, mqttString(clientID)...)
	if hasUser {
		payload = append(payload, mqttString(username)...)
	}
	if hasPass {
		payload = append(payload, mqttString(password)...)
	}
	if err := c.writePacket(msgCONNECT, 0, payload); err != nil {
		return err
	}
	// 等待 CONNACK
	hdr, body, err := c.readPacket()
	if err != nil {
		return err
	}
	if hdr>>4 != msgCONNACK {
		return errors.New("MQTT CONNECT 响应异常")
	}
	if len(body) < 2 || body[1] != 0 {
		return fmt.Errorf("MQTT CONNECT 被拒绝，code=%d", connectReturnCode(body))
	}
	c.connected.Store(true)
	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	go c.readLoop(ctx)
	return nil
}

// Subscribe 订阅主题（Qos0）
func (c *Client) Subscribe(topics ...string) error {
	if !c.connected.Load() {
		return errors.New("MQTT 未连接")
	}
	packetID := uint16(time.Now().UnixNano() & 0xffff)
	if packetID == 0 {
		packetID = 1
	}
	payload := uint16Bytes(packetID)
	for _, t := range topics {
		payload = append(payload, mqttString(t)...)
		payload = append(payload, 0) // requested Qos 0
	}
	return c.writePacket(msgSUBSCRIBE, 0x02, payload)
}

// Publish 发布消息（Qos0）
func (c *Client) Publish(topic string, payload []byte) error {
	if !c.connected.Load() {
		return errors.New("MQTT 未连接")
	}
	body := mqttString(topic)
	body = append(body, payload...)
	return c.writePacket(msgPUBLISH, 0, body)
}

// Ping 发送 PINGREQ 保活
func (c *Client) Ping() error {
	return c.writePacket(msgPINGREQ, 0, nil)
}

// Disconnect 优雅断开
func (c *Client) Disconnect() {
	_ = c.writePacket(msgDISCONNECT, 0, nil)
	if c.cancel != nil {
		c.cancel()
	}
	c.connected.Store(false)
	_ = c.conn.Close()
}

// Close 立即关闭连接
func (c *Client) Close() error {
	c.connected.Store(false)
	if c.cancel != nil {
		c.cancel()
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return c.conn.Close()
}

// Connected 是否处于连接状态
func (c *Client) Connected() bool { return c.connected.Load() }

func (c *Client) readLoop(ctx context.Context) {
	go c.keepAlive(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		hdr, body, err := c.readPacket()
		if err != nil {
			c.connected.Store(false)
			return
		}
		typ := hdr >> 4
		switch typ {
		case msgPINGRESP, msgSUBACK, msgCONNACK:
			// 仅确认包，无需处理
		case msgPUBLISH:
			if c.OnMessage != nil {
				topic, payload := parsePublish(hdr, body)
				c.OnMessage(topic, payload)
			}
		}
	}
}

func (c *Client) keepAlive(ctx context.Context) {
	if c.keepalive <= 0 {
		return
	}
	t := time.NewTicker(time.Duration(c.keepalive) * time.Second / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Ping(); err != nil {
				return
			}
		}
	}
}

// ---- 协议编码 ----

func (c *Client) writePacket(typ byte, flags byte, body []byte) error {
	header := typ<<4 | flags
	pkt := []byte{header}
	pkt = append(pkt, encodeVarInt(len(body))...)
	pkt = append(pkt, body...)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("MQTT 连接已关闭")
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	_, err := c.conn.Write(pkt)
	return err
}

// readPacket 读取一个完整 MQTT 数据包
func (c *Client) readPacket() (byte, []byte, error) {
	header, err := c.br.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	rl, err := decodeVarInt(c.br)
	if err != nil {
		return 0, nil, err
	}
	if rl < 0 || rl > 1024*1024*64 {
		return 0, nil, errors.New("非法 MQTT 包长度")
	}
	body := make([]byte, rl)
	if _, err := readFull(c.br, body); err != nil {
		return 0, nil, err
	}
	return header, body, nil
}

// ---- 工具 ----

func mqttString(s string) []byte {
	b := []byte(s)
	out := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(out, uint16(len(b)))
	copy(out[2:], b)
	return out
}

func uint16Bytes(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func encodeVarInt(n int) []byte {
	var out []byte
	for {
		digit := byte(n % 128)
		n /= 128
		if n > 0 {
			digit |= 0x80
		}
		out = append(out, digit)
		if n == 0 {
			return out
		}
	}
}

func decodeVarInt(br *bufio.Reader) (int, error) {
	multiplier := 1
	value := 0
	for i := 0; i < 4; i++ {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		value += int(b&0x7f) * multiplier
		if b&0x80 == 0 {
			return value, nil
		}
		multiplier *= 128
	}
	return 0, errors.New("MQTT 可变长度编码错误")
}

func readFull(br *bufio.Reader, body []byte) (int, error) {
	return readFullImpl(br, body)
}

// parsePublish 解析 PUBLISH 体，返回 topic 与 payload
func parsePublish(header byte, body []byte) (string, []byte) {
	qos := (header >> 1) & 0x03
	off := 0
	topicLen := int(binary.BigEndian.Uint16(body[off:]))
	off += 2
	topic := string(body[off : off+topicLen])
	off += topicLen
	if qos > 0 {
		off += 2 // 跳过 packet ID
	}
	return topic, body[off:]
}

func connectReturnCode(body []byte) int {
	if len(body) >= 2 {
		return int(body[1])
	}
	return -1
}

// brokerHostPort 从 broker 地址解析 host/port
func brokerHostPort(broker string) (string, string) {
	s := strings.TrimSpace(broker)
	if u, err := url.Parse(s); err == nil && u.Scheme != "" {
		if u.Port() != "" {
			return u.Hostname(), u.Port()
		}
		return u.Hostname(), "1883"
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		return h, p
	}
	return s, "1883"
}

func readFullImpl(br *bufio.Reader, body []byte) (int, error) {
	n := 0
	for n < len(body) {
		m, err := br.Read(body[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}