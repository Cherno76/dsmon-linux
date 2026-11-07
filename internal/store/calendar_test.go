package store

import (
	"testing"
	"time"
)

func calendarDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, rangeLocation)
}

func TestCalendarCellsAlignWeekdays(t *testing.T) {
	august := MonthStart(calendarDate(2026, time.August, 8), rangeLocation)
	cells := CalendarCells(august, rangeLocation, time.Sunday)
	if len(cells)%7 != 0 || len(cells) != 42 {
		t.Fatalf("August cells = %d", len(cells))
	}
	for i := 0; i < 6; i++ {
		if cells[i] != nil {
			t.Fatalf("August leading cell %d = %v", i, cells[i])
		}
	}
	if cells[6] == nil || !cells[6].Equal(august) {
		t.Fatalf("August first day cell = %v", cells[6])
	}
	september := MonthStart(calendarDate(2026, time.September, 8), rangeLocation)
	cells = CalendarCells(september, rangeLocation, time.Sunday)
	for i := 0; i < 2; i++ {
		if cells[i] != nil {
			t.Fatalf("September leading cell %d = %v", i, cells[i])
		}
	}
}

func TestCalendarCellsLeapFebruary(t *testing.T) {
	february := MonthStart(calendarDate(2028, time.February, 10), rangeLocation)
	cells := CalendarCells(february, rangeLocation, time.Sunday)
	days := 0
	for _, cell := range cells {
		if cell != nil {
			days++
			if !cell.Equal(startOfDay(*cell, rangeLocation)) {
				t.Fatalf("cell is not midnight: %v", cell)
			}
		}
	}
	if days != 29 {
		t.Fatalf("leap February days = %d", days)
	}
}

func TestWeekdaySymbolsFollowFirstWeekday(t *testing.T) {
	sunday := WeekdaySymbols(time.Sunday)
	monday := WeekdaySymbols(time.Monday)
	if len(sunday) != 7 || len(monday) != 7 || sunday[0] != "日" || monday[0] != "一" {
		t.Fatalf("symbols sunday=%v monday=%v", sunday, monday)
	}
}

func TestMonthArithmetic(t *testing.T) {
	december := calendarDate(2026, time.December, 1)
	if got := MonthOffset(december, 1, rangeLocation); !got.Equal(calendarDate(2027, time.January, 1)) {
		t.Fatalf("+1 month = %v", got)
	}
	if got := MonthOffset(december, -1, rangeLocation); !got.Equal(calendarDate(2026, time.November, 1)) {
		t.Fatalf("-1 month = %v", got)
	}
	if got := MonthOffset(calendarDate(2026, time.March, 15), 0, rangeLocation); !got.Equal(calendarDate(2026, time.March, 1)) {
		t.Fatalf("month start = %v", got)
	}
}

func TestCalendarRangeNeedsTwoClicks(t *testing.T) {
	first := calendarDate(2026, time.September, 10)
	if CalendarRange(nil, first, rangeLocation) != nil {
		t.Fatal("one click should not create a range")
	}
	forward := CalendarRange(&first, calendarDate(2026, time.September, 16), rangeLocation)
	if forward == nil || !forward.Start.Equal(first) || !forward.End.Equal(calendarDate(2026, time.September, 17)) {
		t.Fatalf("forward range = %+v", forward)
	}
	backward := CalendarRange(ptrTime(calendarDate(2026, time.September, 16)), first, rangeLocation)
	if backward == nil || !backward.Start.Equal(forward.Start) || !backward.End.Equal(forward.End) {
		t.Fatalf("backward range = %+v", backward)
	}
	same := CalendarRange(&first, first, rangeLocation)
	if same == nil || same.Duration() != 24*time.Hour {
		t.Fatalf("same day range = %+v", same)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
