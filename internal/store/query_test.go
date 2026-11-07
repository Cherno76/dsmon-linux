package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestPaginationOrderAndCount(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"),
		rules(1, 0.1, 2, "deepseek-flash"))
	base := time.Date(2026, time.September, 16, 10, 0, 0, 0, testLocation)
	for index := 0; index < 3; index++ {
		if err := database.Insert(ctx, recordAt(base.Add(time.Duration(index)*time.Hour), "deepseek-flash")); err != nil {
			t.Fatalf("insert %d: %v", index, err)
		}
	}
	total, err := database.RequestCount(ctx)
	if err != nil || total != 3 {
		t.Fatalf("count = %d, err = %v", total, err)
	}
	first, err := database.Requests(ctx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first page rows = %d", len(first))
	}
	if !first[0].Timestamp.Equal(base.Add(2*time.Hour)) || !first[1].Timestamp.Equal(base.Add(time.Hour)) {
		t.Fatalf("first page order = %v, %v", first[0].Timestamp, first[1].Timestamp)
	}
	if first[0].Client != "opencode" || first[0].TotalTokens != 1_100 {
		t.Fatalf("first page row = %+v", first[0])
	}
	second, err := database.Requests(ctx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || !second[0].Timestamp.Equal(base) {
		t.Fatalf("second page = %+v", second)
	}
	if zero, err := database.Requests(ctx, 2, 9); err != nil || len(zero) != 0 {
		t.Fatalf("out-of-range page = %+v, err = %v", zero, err)
	}
}
