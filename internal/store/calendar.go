package store

import "time"

// MonthStart 返回某个月的 1 号零点。
func MonthStart(at time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	local := at.In(loc)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
}

// MonthOffset 相对某个月挪动，结果仍是那个月的 1 号。
func MonthOffset(month time.Time, offset int, loc *time.Location) time.Time {
	return MonthStart(month, loc).AddDate(0, offset, 0)
}

// CalendarCells 生成对齐到整周的月历网格；空格用 nil 表示。
func CalendarCells(month time.Time, loc *time.Location, firstWeekday time.Weekday) []*time.Time {
	if loc == nil {
		loc = time.Local
	}
	first := MonthStart(month, loc)
	next := first.AddDate(0, 1, 0)
	leading := (int(first.Weekday()) - int(firstWeekday) + 7) % 7
	cells := make([]*time.Time, 0, 42)
	for i := 0; i < leading; i++ {
		cells = append(cells, nil)
	}
	for day := first; day.Before(next); day = day.AddDate(0, 0, 1) {
		value := day
		cells = append(cells, &value)
	}
	for len(cells)%7 != 0 {
		cells = append(cells, nil)
	}
	return cells
}

// WeekdaySymbols 返回按 firstWeekday 轮转后的表头。
func WeekdaySymbols(firstWeekday time.Weekday) []string {
	symbols := []string{"日", "一", "二", "三", "四", "五", "六"}
	shift := int(firstWeekday) % 7
	return append(append([]string{}, symbols[shift:]...), symbols[:shift]...)
}

// CalendarRange 把两次点击变成一个整天对齐的区间；只点一次时返回 nil。
func CalendarRange(anchor *time.Time, picked time.Time, loc *time.Location) *TimeRange {
	if anchor == nil {
		return nil
	}
	value := CustomInterval(*anchor, picked, loc)
	return &value
}
