//go:build !windows

package daemon

import "log"

// wakeMonitor no-op on non-Windows (Pi에는 모니터 없음)
func wakeMonitor() {
	log.Println("[DAEMON] wakeMonitor: no-op on this platform")
}

// sleepPC no-op on non-Windows (Pi는 24/7 가동)
func sleepPC() {
	log.Println("[DAEMON] sleepPC: disabled on this platform")
}

// getUserIdleSeconds 항상 유휴 상태 반환 (non-Windows)
func getUserIdleSeconds() int {
	return 9999
}
