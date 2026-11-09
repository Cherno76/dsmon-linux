package trace

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTrafficBucketSeconds = 2 * time.Second
	DefaultTrafficBucketCount   = 3600
	TrafficRateWindow           = 2 * time.Second
	DefaultTrafficGridSeconds   = 30 * time.Second
	MinimumScale                = 1024.0
	PeakHeadroom                = 1.25
	TrafficShiftQuantum         = 0.25
	UsesLogarithmicScale        = false
	LogDecades                  = 1000.0
)

type Bucket struct {
	Upload   int
	Download int
}

func (b Bucket) Total() int { return b.Upload + b.Download }

// Traffic 是上下行两条独立计数的绝对时间环。
type Traffic struct {
	bucketSeconds time.Duration
	bucketCount   int
	buckets       []Bucket
	anchor        int64
	uploadTotal   int64
	downloadTotal int64
	uploadTop     float64
	downloadTop   float64
}

type TrafficStats struct {
	UploadTotal   int64
	DownloadTotal int64
	UploadTop     float64
	DownloadTop   float64
}

func NewTraffic(now time.Time) *Traffic {
	return NewTrafficWith(DefaultTrafficBucketSeconds, DefaultTrafficBucketCount, now)
}

func NewTrafficWith(bucketSeconds time.Duration, bucketCount int, now time.Time) *Traffic {
	if bucketCount < 1 {
		bucketCount = 1
	}
	return &Traffic{
		bucketSeconds: bucketSeconds,
		bucketCount:   bucketCount,
		buckets:       make([]Bucket, bucketCount),
		anchor:        BucketIndex(now, bucketSeconds) - int64(bucketCount-1),
	}
}

func (t *Traffic) BucketSeconds() time.Duration { return t.bucketSeconds }
func (t *Traffic) BucketCount() int             { return t.bucketCount }
func (t *Traffic) Anchor() int64                { return t.anchor }
func (t *Traffic) CurrentIndex() int64          { return t.anchor + int64(t.bucketCount-1) }
func (t *Traffic) Buckets() []Bucket            { return append([]Bucket(nil), t.buckets...) }

func (t *Traffic) Stats() TrafficStats {
	return TrafficStats{
		UploadTotal: t.uploadTotal, DownloadTotal: t.downloadTotal,
		UploadTop: t.uploadTop, DownloadTop: t.downloadTop,
	}
}

func (t *Traffic) Advance(to time.Time) {
	target := BucketIndex(to, t.bucketSeconds)
	shift := target - t.CurrentIndex()
	if shift <= 0 {
		return
	}
	if shift >= int64(t.bucketCount) {
		t.buckets = make([]Bucket, t.bucketCount)
		t.anchor = target - int64(t.bucketCount-1)
		return
	}
	copy(t.buckets, t.buckets[shift:])
	for index := t.bucketCount - int(shift); index < t.bucketCount; index++ {
		t.buckets[index] = Bucket{}
	}
	t.anchor += shift
}

func (t *Traffic) Record(upload, download int, now time.Time) {
	if upload <= 0 && download <= 0 {
		return
	}
	t.Advance(now)
	span := t.bucketSeconds.Seconds()
	if span <= 0 {
		span = 1
	}
	if upload > 0 {
		t.buckets[t.bucketCount-1].Upload += upload
		t.uploadTotal += int64(upload)
		t.uploadTop = math.Max(t.uploadTop, float64(t.buckets[t.bucketCount-1].Upload)/span)
	}
	if download > 0 {
		t.buckets[t.bucketCount-1].Download += download
		t.downloadTotal += int64(download)
		t.downloadTop = math.Max(t.downloadTop, float64(t.buckets[t.bucketCount-1].Download)/span)
	}
}

func (t *Traffic) Snapshot(now time.Time) []Bucket {
	copyTrace := *t
	copyTrace.buckets = append([]Bucket(nil), t.buckets...)
	copyTrace.Advance(now)
	return copyTrace.buckets
}

type Frame struct {
	Buckets       []Bucket
	BucketSeconds time.Duration
	Shift         float64
	GridLines     []float64
	DownRate      float64
	UpRate        float64
	DownScale     float64
	UpScale       float64
	DownPeak      float64
	UpPeak        float64
	DownTop       float64
	UpTop         float64
	DownTotal     int64
	UpTotal       int64
}

func MakeFrame(buckets []Bucket, bucketSeconds time.Duration, now time.Time, blockWidth float64) Frame {
	if bucketSeconds <= 0 {
		bucketSeconds = time.Second
	}
	span := bucketSeconds.Seconds()
	windowBuckets := int(math.Ceil(TrafficRateWindow.Seconds() / span))
	if windowBuckets < 1 {
		windowBuckets = 1
	}
	windowStart := len(buckets) - windowBuckets
	if windowStart < 0 {
		windowStart = 0
	}
	var downRate, upRate float64
	for _, bucket := range buckets[windowStart:] {
		downRate = math.Max(downRate, float64(bucket.Download)/span)
		upRate = math.Max(upRate, float64(bucket.Upload)/span)
	}
	var downPeak, upPeak float64
	for _, bucket := range buckets {
		downPeak = math.Max(downPeak, float64(bucket.Download)/span)
		upPeak = math.Max(upPeak, float64(bucket.Upload)/span)
	}
	phase := BucketPhase(now, bucketSeconds)
	shift := Shift(phase, blockWidth, len(buckets))
	return Frame{
		Buckets:       append([]Bucket(nil), buckets...),
		BucketSeconds: bucketSeconds,
		Shift:         shift,
		GridLines:     GridLines(now, shift, blockWidth, DefaultTrafficGridSeconds, bucketSeconds, len(buckets)),
		DownRate:      downRate,
		UpRate:        upRate,
		DownScale:     ScaleForPeak(downPeak),
		UpScale:       ScaleForPeak(upPeak),
		DownPeak:      downPeak,
		UpPeak:        upPeak,
	}
}

func (f Frame) Ratio(bytes int, scale float64, logarithmic bool) float64 {
	if f.BucketSeconds <= 0 || scale <= 0 {
		return 0
	}
	rate := float64(bytes) / f.BucketSeconds.Seconds()
	if !logarithmic {
		return math.Min(1, math.Max(0, rate/scale))
	}
	bottom := scale / LogDecades
	if rate <= bottom {
		return 0
	}
	return math.Min(1, math.Log10(rate/bottom)/math.Log10(scale/bottom))
}

func (f Frame) IsEmpty() bool {
	for _, bucket := range f.Buckets {
		if bucket.Total() != 0 {
			return false
		}
	}
	return true
}

func (f Frame) DownText() string { return FormatRate(f.DownRate) }
func (f Frame) UpText() string   { return FormatRate(f.UpRate) }

var netByteUnits = []string{
	"Byte", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB", "ZiB", "YiB", "BiB", "GEB",
}

var netBitUnits = []string{
	"bit", "Kib", "Mib", "Gib", "Tib", "Pib", "Eib", "Zib", "Yib", "Bib", "GEb",
}

// humanizeNet 复刻 btop floating_humanizer 的 IEC 单位与两位小数规则。
func humanizeNet(value float64, shorten, bit, perSecond bool) string {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		value = 0
	}
	scaled := int64(math.Round(value * 100))
	if bit {
		scaled *= 8
	}
	units := netByteUnits
	if bit {
		units = netBitUnits
	}
	unit := 0
	for scaled >= 102400 && unit < len(units)-1 {
		scaled >>= 10
		unit++
	}
	text := strconv.FormatInt(scaled, 10)
	switch {
	case len(text) == 4 && unit > 0:
		text = text[:2] + "." + text[2:3]
	case len(text) == 3 && unit > 0:
		text = text[:1] + "." + text[1:]
	case len(text) >= 2:
		text = text[:len(text)-2]
	}
	if text == "" {
		text = "0"
	}
	if shorten {
		hasSeparator := strings.Contains(text, ".")
		if hasSeparator {
			if parsed, err := strconv.ParseFloat(text, 64); err == nil {
				text = fmt.Sprintf("%.1f", parsed)
			}
		}
		if len(text) > 3 {
			if hasSeparator {
				if parsed, err := strconv.ParseFloat(text, 64); err == nil {
					text = fmt.Sprintf("%.0f", parsed)
				}
			} else {
				text = text[:1] + ".0"
				unit = min(unit+1, len(units)-1)
			}
		}
		text += units[unit][:1]
	} else {
		text += " " + units[unit]
	}
	if perSecond {
		if bit {
			text += "ps"
		} else {
			text += "/s"
		}
	}
	return text
}

func FormatNetSpeed(bytesPerSecond float64, bits bool) string {
	return humanizeNet(bytesPerSecond, false, bits, true)
}

func FormatNetTotal(bytes float64) string {
	return humanizeNet(bytes, false, false, false)
}

func FormatNetScale(bytesPerSecond float64) string {
	value := NiceScale(bytesPerSecond)
	units := []string{"B", "K", "M", "G", "T", "P", "E"}
	unit := 0
	for value >= 1000 && unit < len(units)-1 {
		value /= 1000
		unit++
	}
	if unit == 0 {
		return strconv.FormatFloat(value, 'f', 0, 64) + units[unit]
	}
	if value < 10 {
		return strconv.FormatFloat(value, 'f', 1, 64) + units[unit]
	}
	return strconv.FormatFloat(value, 'f', 0, 64) + units[unit]
}

func ScaleForPeak(peak float64) float64 {
	if math.IsNaN(peak) || math.IsInf(peak, 0) || peak <= 0 {
		return MinimumScale
	}
	wanted := peak * PeakHeadroom
	if wanted <= MinimumScale {
		return MinimumScale
	}
	scale := NiceScale(wanted)
	if scale < peak {
		step := scaleStep(wanted)
		scale = math.Ceil(peak/step) * step
	}
	if scale < MinimumScale {
		return MinimumScale
	}
	return scale
}

func NiceScale(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return MinimumScale
	}
	step := scaleStep(value)
	rounded := math.Round(value/step) * step
	if rounded < MinimumScale {
		return MinimumScale
	}
	return rounded
}

func NiceScaleBelow(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value <= MinimumScale {
		return 0
	}
	step := scaleStep(value - 1)
	rounded := math.Floor((value-1e-9)/step) * step
	if rounded < MinimumScale {
		return 0
	}
	return rounded
}

// ScaleUnifyRatio 是上下行量程合并的阈值：较大值小于较小值的两倍时，
// 两条曲线共用较大值，避免 60K/70K 这种只差一档的双轴看起来很乱。
// 正好 2 倍（如 50K/100K）保持独立。
const ScaleUnifyRatio = 2.0

func UnifyScales(down, up float64) (float64, float64) {
	if down <= 0 || up <= 0 {
		return down, up
	}
	larger := math.Max(down, up)
	smaller := math.Min(down, up)
	if larger < smaller*ScaleUnifyRatio {
		return larger, larger
	}
	return down, up
}

// scaleStep 是量程的就近取整步长。量程以 K 为单位时，100K 以下用 10K，
// 100K–1M 用 50K；更低和更高的档位沿用同样的十进制粒度，避免 1.5K 被
// 粗暴取整成 0 或 10K。M/G 档按同样比例放大。
func scaleStep(value float64) float64 {
	switch {
	case value < 10_000:
		return 500
	case value < 100_000:
		return 10_000
	case value < 1_000_000:
		return 50_000
	case value < 10_000_000:
		return 500_000
	case value < 100_000_000:
		return 5_000_000
	default:
		return 50_000_000
	}
}

func Shift(phase, blockWidth float64, bucketCount int) float64 {
	slot := blockWidth / float64(maxInt(1, bucketCount-1))
	raw := math.Min(math.Max(phase, 0), 1) * slot
	return math.Round(raw/TrafficShiftQuantum) * TrafficShiftQuantum
}

func GridLines(now time.Time, shift, blockWidth float64, spacing, bucketSeconds time.Duration,
	bucketCount int) []float64 {
	if spacing <= 0 || bucketSeconds <= 0 || bucketCount <= 1 {
		return nil
	}
	slot := blockWidth / float64(bucketCount-1)
	epoch := float64(now.UnixNano()) / 1e9
	if math.IsNaN(epoch) || math.IsInf(epoch, 0) {
		return nil
	}
	blockStart := math.Floor(epoch/bucketSeconds.Seconds()) * bucketSeconds.Seconds()
	tick := math.Floor(epoch/spacing.Seconds()) * spacing.Seconds()
	var lines []float64
	for {
		ageBuckets := (blockStart - tick) / bucketSeconds.Seconds()
		x := blockWidth - ageBuckets*slot - shift
		if x < 0 {
			break
		}
		if x <= blockWidth+TrafficShiftQuantum {
			lines = append(lines, x)
		}
		tick -= spacing.Seconds()
	}
	for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
		lines[left], lines[right] = lines[right], lines[left]
	}
	return lines
}

type ScalePair struct{ Down, Up float64 }

type Ratchet struct {
	down          float64
	up            float64
	downQuietFrom *time.Time
	upQuietFrom   *time.Time
}

func (r *Ratchet) Scales(downPeak, upPeak float64, now time.Time) ScalePair {
	r.down = ratchetStep(r.down, downPeak, &r.downQuietFrom, now)
	r.up = ratchetStep(r.up, upPeak, &r.upQuietFrom, now)
	return ScalePair{Down: r.down, Up: r.up}
}

func ratchetStep(shown, peak float64, quietFrom **time.Time, now time.Time) float64 {
	if peak > shown {
		*quietFrom = nil
		return ScaleForPeak(peak)
	}
	if shown <= 0 {
		*quietFrom = nil
		return MinimumScale
	}
	if peak >= shown*0.25 {
		*quietFrom = nil
		return shown
	}
	if *quietFrom == nil {
		value := now
		*quietFrom = &value
		return shown
	}
	if now.Sub(**quietFrom) < 20*time.Second {
		return shown
	}
	value := now
	*quietFrom = &value
	return math.Max(MinimumScale, NiceScaleBelow(shown))
}

type RatePair struct{ Down, Up float64 }

type RateHold struct {
	down      float64
	up        float64
	changedAt *time.Time
}

func (h *RateHold) Apply(rawDown, rawUp float64, now time.Time) RatePair {
	beforeDown, beforeUp := h.down, h.up
	h.down = holdStep(h.down, rawDown, h.changedAt, now)
	h.up = holdStep(h.up, rawUp, h.changedAt, now)
	if h.down != beforeDown || h.up != beforeUp {
		value := now
		h.changedAt = &value
	}
	return RatePair{Down: h.down, Up: h.up}
}

func holdStep(shown, raw float64, changedAt *time.Time, now time.Time) float64 {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return shown
	}
	if raw < 10 {
		return 0
	}
	if shown < 10 {
		return raw
	}
	if changedAt != nil && now.Sub(*changedAt) < 2*time.Second {
		return shown
	}
	if math.Abs(raw-shown)/shown >= 0.25 {
		return raw
	}
	return shown
}

func FormatRate(rate float64) string {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		rate = 0
	}
	if rate >= 1024*1024 {
		return trimRate(rate/(1024*1024)) + "M/s"
	}
	if rate >= 1024 {
		return trimRate(rate/1024) + "K/s"
	}
	return fmt.Sprintf("%.0fB/s", rate)
}

func trimRate(value float64) string {
	if value < 10 {
		return fmt.Sprintf("%.1f", value)
	}
	return fmt.Sprintf("%.0f", math.Round(value))
}

func maxInt(first, second int) int {
	if first > second {
		return first
	}
	return second
}
