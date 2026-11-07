package pricing

import "time"

// BeijingHour 返回北京时间（UTC+8，无夏令时）的小时。
func BeijingHour(at time.Time) int {
	seconds := at.Unix()
	return int(((seconds+8*3600)%86400+86400)%86400) / 3600
}

// IsBeijingWeekend 判断北京时间是否周六或周日。1970-01-01 是周四。
func IsBeijingWeekend(at time.Time) bool {
	seconds := at.Unix()
	day := floorDiv(seconds+8*3600, 86400)
	dow := ((day+4)%7 + 7) % 7
	return dow == 0 || dow == 6
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
