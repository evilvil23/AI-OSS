// usb.go USB 移动存储设备监控（Windows）。
//
// 通过周期枚举 Win32_LogicalDisk（DriveType=2 可移动磁盘）检测设备接入：
//   - 新盘符出现 → 视为设备接入，回调 deviceID；
//   - deviceID = 串号（优先）| 卷标，用于与任务绑定的 usb_device_id 匹配；
//   - 陌生 U 盘（未绑定任何任务）不会触发备份。
package backup

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"smart-nas/pkg/logger"
)

// USBWatcher USB 设备监控器
type USBWatcher struct {
	onDevice func(deviceID string) // 设备接入回调
	stop     chan struct{}
	stopOnce sync.Once
	interval time.Duration
}

// NewUSBWatcher 创建监控器（interval<=0 取 3 秒）
func NewUSBWatcher(onDevice func(deviceID string)) *USBWatcher {
	return &USBWatcher{onDevice: onDevice, stop: make(chan struct{}), interval: 3 * time.Second}
}

// Start 启动轮询
func (w *USBWatcher) Start() {
	if w.onDevice == nil {
		return
	}
	go func() {
		known := map[string]struct{}{}
		// 启动时已插入的设备视为已知（不触发）
		for _, id := range listUSBDeviceIDs() {
			known[id] = struct{}{}
		}
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				current := listUSBDeviceIDs()
				for _, id := range current {
					if _, ok := known[id]; !ok {
						logger.Info("[backup] 检测到 USB 设备接入", "device", id)
						w.onDevice(id)
					}
				}
				known = make(map[string]struct{}, len(current))
				for _, id := range current {
					known[id] = struct{}{}
				}
			}
		}
	}()
}

// Stop 停止轮询
func (w *USBWatcher) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
}

// listUSBDeviceIDs 枚举可移动磁盘的设备标识（串号|卷标，可能为空则用盘符）。
// Windows 实现：枚举 A-Z，GetDriveTypeW==DRIVE_REMOVABLE(2) 的盘符，
// 再经 GetVolumeInformationW 取卷标、DeviceIoControl(IOCTL_STORAGE_QUERY_PROPERTY) 取串号。
func listUSBDeviceIDs() []string {
	if !isWindows() {
		return nil
	}
	var out []string
	for letter := 'A'; letter <= 'Z'; letter++ {
		root := string(letter) + ":\\"
		if !isRemovableDrive(root) {
			continue
		}
		label, serial := volumeInfo(root)
		id := serial
		if id == "" {
			id = label
		}
		if id == "" {
			id = string(letter) + ":"
		} else if label != "" && label != serial {
			id = id + "|" + label
		}
		out = append(out, id)
	}
	return out
}

// isWindows 平台判断（变量形式便于测试注入）
var isWindows = func() bool { return os.PathSeparator == '\\' }

// GetUSBDeviceID 读取指定路径所在可移动磁盘的设备标识（供前端「选择设备」用）
func GetUSBDeviceID(path string) string {
	root := filepath.VolumeName(path) + "\\"
	if !isRemovableDrive(root) {
		return ""
	}
	label, serial := volumeInfo(root)
	id := serial
	if id == "" {
		id = label
	}
	if id == "" {
		id = filepath.VolumeName(path)
	}
	return id
}

// USBDevice 当前接入的可移动设备信息
type USBDevice struct {
	ID    string `json:"id"`    // 串号|卷标（绑定用）
	Label string `json:"label"` // 卷标
	Mount string `json:"mount"` // 盘符根（如 F:\）
}

// ListUSBDevices 列出当前接入的可移动设备（供前端绑定选择）
func ListUSBDevices() []USBDevice {
	if !isWindows() {
		return nil
	}
	var out []USBDevice
	for letter := 'A'; letter <= 'Z'; letter++ {
		root := string(letter) + ":\\"
		if !isRemovableDrive(root) {
			continue
		}
		label, serial := volumeInfo(root)
		id := serial
		if id == "" {
			id = label
		}
		if id == "" {
			id = string(letter) + ":"
		} else if label != "" && label != serial {
			id = id + "|" + label
		}
		out = append(out, USBDevice{ID: id, Label: label, Mount: root})
	}
	return out
}

// ---- Windows API（LazyDLL，与 util/system.go 同风格）----

func isRemovableDrive(root string) bool {
	k := strings.ToLower(root)
	if len(k) < 2 || k[1] != ':' {
		return false
	}
	return driveType(root) == 2 // DRIVE_REMOVABLE
}

func driveType(root string) int {
	kernel32 := syscallNewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDriveTypeW")
	rp, err := utf16PtrFromString(root)
	if err != nil {
		return 0
	}
	r, _, _ := proc.Call(unsafePointerOf(rp))
	return int(r)
}

func volumeInfo(root string) (label, serial string) {
	kernel32 := syscallNewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetVolumeInformationW")
	rp, err := utf16PtrFromString(root)
	if err != nil {
		return "", ""
	}
	var nameBuf [261]uint16
	var serialNum, maxLen, flags uint32
	ok, _, _ := proc.Call(
		unsafePointerOf(rp),
		unsafePointerOf(&nameBuf[0]),
		uintptr(len(nameBuf)),
		unsafePointerOf(&serialNum),
		unsafePointerOf(&maxLen),
		unsafePointerOf(&flags),
		0, 0,
	)
	if ok == 0 {
		return "", ""
	}
	label = utf16ToString(nameBuf[:])
	serial = strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(fmtSerial(serialNum)), ""))
	return label, serial
}

func fmtSerial(n uint32) string {
	// 输出形如 "1A2B3C4D"
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = hexDigits[n&0xF]
		n >>= 4
	}
	return string(out)
}
