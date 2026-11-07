package trace

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func newTestTraffic(now time.Time) *Traffic {
	return NewTrafficWith(time.Second, 4, now)
}

func TestTrafficDirectionsAndZeroRecords(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	traffic := newTestTraffic(at)
	traffic.Record(100, 300, at)
	traffic.Record(50, 7, at.Add(900*time.Millisecond))
	want := []Bucket{{}, {}, {}, {Upload: 150, Download: 307}}
	if got := traffic.Buckets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("buckets = %+v, want %+v", got, want)
	}
	index := traffic.CurrentIndex()
	traffic.Record(0, 0, at.Add(3*time.Second))
	if traffic.CurrentIndex() != index {
		t.Fatal("zero-byte record advanced the window")
	}
}

func TestTrafficGapsSnapshotAndReset(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	traffic := newTestTraffic(at)
	traffic.Record(0, 10, at)
	traffic.Record(0, 20, at.Add(2*time.Second))
	want := []Bucket{{}, {Download: 10}, {}, {Download: 20}}
	if got := traffic.Buckets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("buckets = %+v", got)
	}
	if got := traffic.Snapshot(at.Add(3 * time.Second)); !reflect.DeepEqual(got, []Bucket{{Download: 10}, {}, {Download: 20}, {}}) {
		t.Fatalf("snapshot = %+v", got)
	}
	if got := traffic.Buckets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mutated trace: %+v", got)
	}
	traffic.Record(0, 30, at.Add(9*time.Second))
	if got := traffic.Buckets(); !reflect.DeepEqual(got, []Bucket{{}, {}, {}, {Download: 30}}) {
		t.Fatalf("gap reset = %+v", got)
	}
}

func TestTrafficRateUsesBusiestOfLastTwoBuckets(t *testing.T) {
	frame := MakeFrame([]Bucket{
		{Upload: 9000, Download: 9000},
		{Download: 3000},
		{Upload: 2048},
	}, time.Second, time.Unix(1_700_000_000, 0), 52.5)
	if frame.UpRate != 2048 || frame.DownRate != 3000 {
		t.Fatalf("rates up=%v down=%v", frame.UpRate, frame.DownRate)
	}
	if frame.UpText() != "2.0K/s" || frame.DownText() != "2.9K/s" {
		t.Fatalf("rate text up=%q down=%q", frame.UpText(), frame.DownText())
	}
}

func TestTrafficDefaultsMatchBtopDensity(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	traffic := NewTraffic(at)
	if traffic.BucketCount() != 3600 || traffic.BucketSeconds() != 500*time.Millisecond {
		t.Fatalf("default traffic ring = %d x %s", traffic.BucketCount(), traffic.BucketSeconds())
	}
	if window := time.Duration(traffic.BucketCount()) * traffic.BucketSeconds(); window != 30*time.Minute {
		t.Fatalf("default traffic window = %s", window)
	}
}

func TestTrafficRateWindowStaysTwoSeconds(t *testing.T) {
	frame := MakeFrame([]Bucket{
		{Upload: 9000, Download: 9000},
		{Download: 3000},
		{Download: 2000},
		{Upload: 2048},
		{Upload: 1024},
	}, 500*time.Millisecond, time.Unix(1_700_000_000, 0), 120)
	if frame.UpRate != 4096 || frame.DownRate != 6000 {
		t.Fatalf("rates up=%v down=%v", frame.UpRate, frame.DownRate)
	}
}

func TestNiceScaleLadder(t *testing.T) {
	tests := []struct{ input, want float64 }{
		{1_024, 1_024}, {1_500, 1_500}, {2_000, 2_000}, {7_500, 7_500},
		{10_000, 10_000}, {15_000, 20_000}, {29_000, 30_000}, {75_000, 80_000},
		{98_000, 100_000}, {103_000, 100_000}, {147_000, 150_000}, {150_000, 150_000},
		{1_000_000, 1_000_000}, {1_500_000, 1_500_000}, {12_500_000, 15_000_000},
	}
	for _, test := range tests {
		if got := NiceScale(test.input); math.Abs(got-test.want) > 1e-9 {
			t.Fatalf("niceScale(%v) = %v, want %v", test.input, got, test.want)
		}
	}
	if got := ScaleForPeak(9_400_000); got != 10_000_000 {
		t.Fatalf("scale for 9.4M = %v", got)
	}
	if got := ScaleForPeak(100); got != MinimumScale {
		t.Fatalf("minimum scale = %v", got)
	}
}

func TestUnifyNearbyScales(t *testing.T) {
	tests := []struct {
		name             string
		down, up         float64
		wantDown, wantUp float64
	}{
		{"close", 60_000, 70_000, 70_000, 70_000},
		{"close reversed", 70_000, 60_000, 70_000, 70_000},
		{"exact double", 50_000, 100_000, 50_000, 100_000},
		{"under double", 30_000, 50_000, 50_000, 50_000},
		{"over double", 20_000, 50_000, 20_000, 50_000},
		{"idle up", 60_000, 0, 60_000, 0},
		{"idle down", 0, 60_000, 0, 60_000},
	}
	for _, test := range tests {
		down, up := UnifyScales(test.down, test.up)
		if down != test.wantDown || up != test.wantUp {
			t.Fatalf("%s: UnifyScales(%v, %v) = %v/%v, want %v/%v",
				test.name, test.down, test.up, down, up, test.wantDown, test.wantUp)
		}
	}
}

func TestTrafficLanesUseIndependentScales(t *testing.T) {
	frame := MakeFrame([]Bucket{{Upload: 100_000, Download: 1_000}}, time.Second,
		time.Unix(1_800_000_000, 0), 52.5)
	if frame.DownScale != 1500 || frame.UpScale != 150_000 {
		t.Fatalf("scales down=%v up=%v", frame.DownScale, frame.UpScale)
	}
	if got := frame.Ratio(100_000, frame.DownScale, false); got != 1 {
		t.Fatalf("down ratio = %v", got)
	}
	if got := frame.Ratio(100_000, frame.UpScale, false); math.Abs(got-2.0/3.0) > 1e-9 {
		t.Fatalf("up ratio = %v", got)
	}
}

func TestTrafficLogarithmicRatio(t *testing.T) {
	frame := MakeFrame([]Bucket{}, time.Second, time.Unix(1_800_000_000, 0), 52.5)
	scale := 2_000_000.0
	if got := frame.Ratio(int(scale/2), scale, true); math.Abs(got-math.Log10(500)/3) > 1e-9 {
		t.Fatalf("half ratio = %v", got)
	}
	if got := frame.Ratio(int(scale/10), scale, true); math.Abs(got-2.0/3.0) > 1e-9 {
		t.Fatalf("decade ratio = %v", got)
	}
	if got := frame.Ratio(0, scale, true); got != 0 {
		t.Fatalf("zero ratio = %v", got)
	}
}

func TestTrafficRatchet(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	var ratchet Ratchet
	if got := ratchet.Scales(0, 0, at).Down; got != MinimumScale {
		t.Fatalf("idle scale = %v", got)
	}
	if got := ratchet.Scales(300_000, 0, at).Down; got != 400_000 {
		t.Fatalf("growth scale = %v", got)
	}
	if got := ratchet.Scales(2_000_000, 0, at).Down; got != 2_500_000 {
		t.Fatalf("second growth = %v", got)
	}
	ratchet = Ratchet{}
	ratchet.Scales(300_000, 0, at)
	if got := ratchet.Scales(40_000, 0, at.Add(time.Second)).Down; got != 400_000 {
		t.Fatalf("early decay = %v", got)
	}
	if got := ratchet.Scales(40_000, 0, at.Add(19*time.Second)).Down; got != 400_000 {
		t.Fatalf("19s decay = %v", got)
	}
	if got := ratchet.Scales(40_000, 0, at.Add(21*time.Second)).Down; got != 350_000 {
		t.Fatalf("21s decay = %v", got)
	}
	if got := ratchet.Scales(40_000, 0, at.Add(41*time.Second)).Down; got != 300_000 {
		t.Fatalf("second decay = %v", got)
	}
}

func TestTrafficRateHold(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	var hold RateHold
	if got := hold.Apply(100_000, 5_000, at).Down; got != 100_000 {
		t.Fatalf("initial = %v", got)
	}
	if got := hold.Apply(400_000, 5_400, at.Add(time.Second)).Down; got != 100_000 {
		t.Fatalf("dwell = %v", got)
	}
	if got := hold.Apply(112_000, 5_400, at.Add(2*time.Second)).Down; got != 100_000 {
		t.Fatalf("threshold = %v", got)
	}
	if got := hold.Apply(130_000, 5_400, at.Add(2*time.Second)).Down; got != 130_000 {
		t.Fatalf("change = %v", got)
	}
	if got := hold.Apply(5, 5_400, at.Add(2500*time.Millisecond)).Down; got != 0 {
		t.Fatalf("zero snap = %v", got)
	}
	if got := hold.Apply(900, 5_400, at.Add(2600*time.Millisecond)).Down; got != 900 {
		t.Fatalf("restart snap = %v", got)
	}
}

func TestTrafficTracksBtopStats(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	traffic := newTestTraffic(at)
	traffic.Record(100, 300, at)
	traffic.Record(50, 7, at.Add(900*time.Millisecond))
	stats := traffic.Stats()
	if stats.UploadTotal != 150 || stats.DownloadTotal != 307 {
		t.Fatalf("totals = %+v", stats)
	}
	if stats.UploadTop != 150 || stats.DownloadTop != 307 {
		t.Fatalf("tops = %+v", stats)
	}
	traffic.Record(0, 20, at.Add(2*time.Second))
	stats = traffic.Stats()
	if stats.UploadTotal != 150 || stats.DownloadTotal != 327 {
		t.Fatalf("advanced totals = %+v", stats)
	}
}

func TestNetHumanizerMatchesBtopUnits(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"bytes per second", FormatNetSpeed(346, false), "346 Byte/s"},
		{"kibibytes per second", FormatNetSpeed(1331, false), "1.29 KiB/s"},
		// btop 对 bit 速率先做整数移位再插小数点，10.398… 会落成 10.3 而不是四舍五入。
		{"kibibits per second", FormatNetSpeed(1331, true), "10.3 Kibps"},
		{"total", FormatNetTotal(898 * 1024 * 1024 * 1024), "898 GiB"},
		{"scale", FormatNetScale(2 * 1024 * 1024), "2.0M"},
		{"scale 29K", FormatNetScale(29_000), "30K"},
		{"scale 98K", FormatNetScale(98_000), "100K"},
		{"scale 103K", FormatNetScale(103_000), "100K"},
		{"scale 147K", FormatNetScale(147_000), "150K"},
		{"minimum scale", FormatNetScale(MinimumScale), "1.0K"},
		{"zero", FormatNetSpeed(0, false), "0 Byte/s"},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Fatalf("%s = %q, want %q", test.name, test.got, test.want)
		}
	}
}

func TestTrafficGridAndShift(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	frame := MakeFrame(make([]Bucket, 60), time.Second, at, 52.5)
	if len(frame.GridLines) != 2 {
		t.Fatalf("grid lines = %v", frame.GridLines)
	}
	slot := 52.5 / 59
	if math.Abs(frame.GridLines[1]-frame.GridLines[0]-slot*30) > 0.01 {
		t.Fatalf("grid spacing = %v", frame.GridLines)
	}
	half := MakeFrame(make([]Bucket, 60), time.Second, at.Add(500*time.Millisecond), 52.5)
	if math.Abs(half.Shift-slot*0.5) > 0.25 {
		t.Fatalf("shift = %v", half.Shift)
	}
}

func TestFormatRate(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0, "0B/s"}, {777, "777B/s"}, {1024, "1.0K/s"}, {12800, "13K/s"},
		{1048576, "1.0M/s"}, {-5, "0B/s"}, {math.NaN(), "0B/s"}, {math.Inf(1), "0B/s"},
	}
	for _, test := range tests {
		if got := FormatRate(test.input); got != test.want {
			t.Fatalf("FormatRate(%v) = %q, want %q", test.input, got, test.want)
		}
	}
}
