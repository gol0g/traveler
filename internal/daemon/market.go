package daemon

import (
	"fmt"
	"time"
)

// MarketSchedule 미장 스케줄
type MarketSchedule struct {
	// US Eastern Time 기준
	OpenHour   int // 9
	OpenMin    int // 30
	CloseHour  int // 16
	CloseMin   int // 0
}

// DefaultMarketSchedule NYSE/NASDAQ 정규장 시간
func DefaultMarketSchedule() MarketSchedule {
	return MarketSchedule{
		OpenHour:  9,
		OpenMin:   30,
		CloseHour: 16,
		CloseMin:  0,
	}
}

// MarketStatus 마켓 상태
type MarketStatus struct {
	IsOpen        bool
	CurrentTimeET time.Time
	OpenTime      time.Time
	CloseTime     time.Time
	TimeToOpen    time.Duration
	TimeToClose   time.Duration
	Reason        string // "open", "closed", "weekend", "holiday", "pre-market", "after-hours"
}

// GetETLocation US Eastern Time 로케이션
func GetETLocation() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		// Fallback: UTC-5 (EST) or UTC-4 (EDT)
		// 간단히 EST로 가정
		loc = time.FixedZone("EST", -5*60*60)
	}
	return loc
}

// GetMarketStatus 현재 마켓 상태 확인
func GetMarketStatus(schedule MarketSchedule) MarketStatus {
	loc := GetETLocation()
	now := time.Now().In(loc)

	status := MarketStatus{
		CurrentTimeET: now,
	}

	// 오늘 개장/폐장 시간
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	status.OpenTime = today.Add(time.Duration(schedule.OpenHour)*time.Hour + time.Duration(schedule.OpenMin)*time.Minute)
	status.CloseTime = today.Add(time.Duration(schedule.CloseHour)*time.Hour + time.Duration(schedule.CloseMin)*time.Minute)

	// 주말 체크
	weekday := now.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		status.IsOpen = false
		status.Reason = "weekend"

		// 다음 월요일까지 시간
		daysUntilMonday := (8 - int(weekday)) % 7
		if daysUntilMonday == 0 {
			daysUntilMonday = 7
		}
		nextMonday := today.AddDate(0, 0, daysUntilMonday)
		nextOpen := nextMonday.Add(time.Duration(schedule.OpenHour)*time.Hour + time.Duration(schedule.OpenMin)*time.Minute)
		status.TimeToOpen = nextOpen.Sub(now)
		return status
	}

	// 시간대 체크
	currentMinutes := now.Hour()*60 + now.Minute()
	openMinutes := schedule.OpenHour*60 + schedule.OpenMin
	closeMinutes := schedule.CloseHour*60 + schedule.CloseMin

	if currentMinutes < openMinutes {
		// 프리마켓 (장 시작 전)
		status.IsOpen = false
		status.Reason = "pre-market"
		status.TimeToOpen = status.OpenTime.Sub(now)
	} else if currentMinutes >= closeMinutes {
		// 애프터마켓 (장 종료 후)
		status.IsOpen = false
		status.Reason = "after-hours"

		// 다음 거래일 개장까지
		nextDay := today.AddDate(0, 0, 1)
		if nextDay.Weekday() == time.Saturday {
			nextDay = nextDay.AddDate(0, 0, 2) // 월요일로
		} else if nextDay.Weekday() == time.Sunday {
			nextDay = nextDay.AddDate(0, 0, 1)
		}
		nextOpen := time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(),
			schedule.OpenHour, schedule.OpenMin, 0, 0, loc)
		status.TimeToOpen = nextOpen.Sub(now)
	} else {
		// 정규장
		status.IsOpen = true
		status.Reason = "open"
		status.TimeToClose = status.CloseTime.Sub(now)
	}

	return status
}

// IsMarketOpen 마켓 열림 여부
func IsMarketOpen() bool {
	return GetMarketStatus(DefaultMarketSchedule()).IsOpen
}

// WaitForMarketOpen 마켓 열릴 때까지 대기
// 최대 대기 시간 지정 가능, 0이면 무제한
func WaitForMarketOpen(maxWait time.Duration) (bool, MarketStatus) {
	schedule := DefaultMarketSchedule()
	status := GetMarketStatus(schedule)

	if status.IsOpen {
		return true, status
	}

	// 대기 시간이 너무 길면 포기
	if maxWait > 0 && status.TimeToOpen > maxWait {
		return false, status
	}

	// 대기
	if status.TimeToOpen > 0 {
		time.Sleep(status.TimeToOpen)
	}

	return true, GetMarketStatus(schedule)
}

// FormatDuration 시간 포맷팅
func FormatDuration(d time.Duration) string {
	if d < 0 {
		return "0s"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// GetKSTTime 현재 한국 시간
func GetKSTTime() time.Time {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		loc = time.FixedZone("KST", 9*60*60)
	}
	return time.Now().In(loc)
}

// US 공휴일 체크 (간단 버전 - 주요 공휴일만)
var usHolidays2024 = []string{
	"2024-01-01", // New Year's Day
	"2024-01-15", // MLK Day
	"2024-02-19", // Presidents Day
	"2024-03-29", // Good Friday
	"2024-05-27", // Memorial Day
	"2024-06-19", // Juneteenth
	"2024-07-04", // Independence Day
	"2024-09-02", // Labor Day
	"2024-11-28", // Thanksgiving
	"2024-12-25", // Christmas
}

var usHolidays2025 = []string{
	"2025-01-01", // New Year's Day
	"2025-01-20", // MLK Day
	"2025-02-17", // Presidents Day
	"2025-04-18", // Good Friday
	"2025-05-26", // Memorial Day
	"2025-06-19", // Juneteenth
	"2025-07-04", // Independence Day
	"2025-09-01", // Labor Day
	"2025-11-27", // Thanksgiving
	"2025-12-25", // Christmas
}

var usHolidays2026 = []string{
	"2026-01-01", // New Year's Day
	"2026-01-19", // MLK Day
	"2026-02-16", // Presidents Day
	"2026-04-03", // Good Friday
	"2026-05-25", // Memorial Day
	"2026-06-19", // Juneteenth
	"2026-07-03", // Independence Day (observed)
	"2026-09-07", // Labor Day
	"2026-11-26", // Thanksgiving
	"2026-12-25", // Christmas
}

// IsUSHoliday 미국 공휴일 체크
func IsUSHoliday(t time.Time) bool {
	dateStr := t.Format("2006-01-02")

	allHolidays := append(append(usHolidays2024, usHolidays2025...), usHolidays2026...)

	for _, h := range allHolidays {
		if h == dateStr {
			return true
		}
	}
	return false
}
