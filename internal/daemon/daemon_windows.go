//go:build windows

package daemon

import (
	"log"
	"os/exec"
	"syscall"
	"unsafe"
)

// wakeMonitor 모니터 켜기 (Windows: SendMessageW)
func wakeMonitor() {
	user32 := syscall.NewLazyDLL("user32.dll")
	sendMessage := user32.NewProc("SendMessageW")

	// SC_MONITORPOWER = 0xF170, -1 = on
	sendMessage.Call(
		0xFFFF,              // HWND_BROADCAST
		0x0112,              // WM_SYSCOMMAND
		0xF170,              // SC_MONITORPOWER
		uintptr(0xFFFFFFFF), // -1 = monitor on
	)

	log.Println("[DAEMON] Monitor wake signal sent")
}

// sleepPC PC 절전 모드 (Windows: PowerShell SetSuspendState)
func sleepPC() {
	cmd := exec.Command("powershell", "-Command",
		"Add-Type -Assembly System.Windows.Forms; [System.Windows.Forms.Application]::SetSuspendState('Suspend', $false, $false)")
	if err := cmd.Run(); err != nil {
		log.Printf("[DAEMON] Failed to sleep PC: %v", err)
	}
}

// getUserIdleSeconds 사용자 마지막 입력 이후 경과 시간 (초)
func getUserIdleSeconds() int {
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getLastInputInfo := user32.NewProc("GetLastInputInfo")
	getTickCount := kernel32.NewProc("GetTickCount")

	// LASTINPUTINFO: cbSize(4) + dwTime(4) = 8 bytes
	type lastInputInfo struct {
		cbSize uint32
		dwTime uint32
	}
	var info lastInputInfo
	info.cbSize = 8
	ret, _, _ := getLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return 9999 // 실패 시 유휴로 간주
	}
	tick, _, _ := getTickCount.Call()
	idleMs := uint32(tick) - info.dwTime
	return int(idleMs / 1000)
}
