package tui

import (
	"math"
	"strings"
	"time"

	"dsmon/internal/proxy/trace"
)

const brailleBase = 0x2800

var brailleDots = [4][2]rune{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

type brailleCanvas struct {
	width  int
	height int
	cells  []rune
}

func newBrailleCanvas(width, height int) *brailleCanvas {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	return &brailleCanvas{width: width, height: height, cells: make([]rune, width*height)}
}

func (c *brailleCanvas) dot(x, y int) {
	dotsWidth, dotsHeight := c.width*2, c.height*4
	if x < 0 || y < 0 || x >= dotsWidth || y >= dotsHeight {
		return
	}
	index := (y/4)*c.width + x/2
	bit := brailleDots[y%4][x%2]
	if c.cells[index] == 0 {
		c.cells[index] = brailleBase
	}
	c.cells[index] |= bit
}

func (c *brailleCanvas) line(x0, y0, x1, y1 int) {
	dx := absInt(x1 - x0)
	sx := -1
	if x0 < x1 {
		sx = 1
	}
	dy := -absInt(y1 - y0)
	sy := -1
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		c.dot(x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		twice := 2 * err
		if twice >= dy {
			err += dy
			x0 += sx
		}
		if twice <= dx {
			err += dx
			y0 += sy
		}
	}
}

func (c *brailleCanvas) rows() []string {
	rows := make([]string, c.height)
	for y := 0; y < c.height; y++ {
		var builder strings.Builder
		builder.Grow(c.width * 3)
		for x := 0; x < c.width; x++ {
			value := c.cells[y*c.width+x]
			if value == 0 {
				builder.WriteRune(' ')
			} else {
				builder.WriteRune(value)
			}
		}
		rows[y] = builder.String()
	}
	return rows
}

// RenderActivity 按 2 秒桶绘制活动填充面；每个点列一个桶，最新桶贴右。
func RenderActivity(counts []int, width, height int, now time.Time, fullScale int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	canvas := newBrailleCanvas(width, height)
	// 和流量图一样，零基线属于画布本身，不随数据高度或是否有请求而消失。
	for x := 0; x < width*2; x++ {
		canvas.dot(x, height*4-1)
	}
	if len(counts) == 0 || fullScale <= 0 {
		return canvas.rows()
	}
	dotsWidth, dotsHeight := width*2, height*4
	if len(counts) > dotsWidth {
		counts = counts[len(counts)-dotsWidth:]
	}
	offset := maxInt(0, dotsWidth-len(counts))
	phase := trace.BucketPhase(now, trace.DefaultActivityBucketSeconds)

	for x := 0; x < dotsWidth; x++ {
		position := float64(x-offset) + phase
		if position < 0 {
			continue
		}
		value := activityValueAt(counts, position)
		ratio := math.Min(1, math.Max(0, value/float64(fullScale)))
		fill := int(math.Ceil(ratio * float64(dotsHeight)))
		for y := dotsHeight - fill; y < dotsHeight; y++ {
			canvas.dot(x, y)
		}
	}
	return canvas.rows()
}

// activityValueAt 在相邻桶之间用 Catmull-Rom 插值，避免线性折线在峰值处形成尖角。
func activityValueAt(counts []int, position float64) float64 {
	if len(counts) == 0 {
		return 0
	}
	last := len(counts) - 1
	if last == 0 || position <= 0 {
		return float64(counts[0])
	}
	if position >= float64(last) {
		return float64(counts[last])
	}
	left := int(math.Floor(position))
	t := position - float64(left)
	p0 := float64(counts[maxInt(0, left-1)])
	p1 := float64(counts[left])
	p2 := float64(counts[minInt(last, left+1)])
	p3 := float64(counts[minInt(last, left+2)])
	value := 0.5 * (2*p1 +
		(-p0+p2)*t +
		(2*p0-5*p1+4*p2-p3)*t*t +
		(-p0+3*p1-3*p2+p3)*t*t*t)
	return math.Max(0, value)
}

// RenderTraffic 按 btop 的上行/下行双车道绘制流量；height 是两条道的总高度。
func RenderTraffic(frame trace.Frame, width, height int) ([]string, []string) {
	if width <= 0 || height <= 0 {
		return nil, nil
	}
	downHeight := (height + 1) / 2
	upHeight := height - downHeight
	return renderTrafficLane(frame, width, downHeight, false),
		renderTrafficLane(frame, width, upHeight, true)
}

// RenderTrafficNet 按 btop net 的双向镜像布局绘制上下行共用一块盲文画布。
func RenderTrafficNet(frame trace.Frame, width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	canvas := newBrailleCanvas(width, height)
	if len(frame.Buckets) == 0 {
		return canvas.rows()
	}
	downCells := (height + 1) / 2
	upCells := height - downCells
	dotsWidth := width * 2
	dotsHeight := height * 4
	downBaseline := downCells*4 - 1
	upBaseline := downCells * 4
	shift := int(math.Round(frame.Shift))
	span := frame.BucketSeconds.Seconds()
	if span <= 0 {
		span = 1
	}

	downValues := make([]float64, len(frame.Buckets))
	upValues := make([]float64, len(frame.Buckets))
	for index, bucket := range frame.Buckets {
		downValues[index] = float64(bucket.Download)
		upValues[index] = float64(bucket.Upload)
	}
	// btop 的 net deque 只保留 width*2 个样本：每个盲文点列一个样本。
	// 窗口变窄时缩短可见时间窗，而不是把 120 秒全压进几个点列，否则最新尖峰
	// 会退化成一根细线，看起来像停在图中部。
	start := 0
	if dotsWidth < len(downValues) {
		start = len(downValues) - dotsWidth
	}
	downValues = downValues[start:]
	upValues = upValues[start:]
	windowWidth := len(downValues)
	offset := maxInt(0, dotsWidth-windowWidth)

	// 基线属于画布本身，不随历史数据是否填满而变化；右对齐留白时也要横跨全宽。
	for x := 0; x < dotsWidth; x++ {
		canvas.dot(x, downBaseline)
		if upCells > 0 {
			canvas.dot(x, upBaseline)
		}
	}

	// 下载/上传各自从自己的基线填到原始桶高度。中间相邻的两个点行分别是
	// 下载的底边和上传的顶边，和 btop 的两个独立 Graph 一致。
	for x := 0; x < dotsWidth; x++ {
		// 每个桶固定占一个点列；数据不足一屏时右对齐、左侧留空，避免同一个桶
		// 因拉伸在 1/2 个点列之间来回变化。
		sample := x - offset + shift
		if sample < 0 {
			continue
		}
		if sample >= windowWidth {
			continue
		}
		downValue := downValues[sample]
		upValue := upValues[sample]

		downFill := 1
		if frame.DownScale > 0 {
			ratio := math.Min(1, math.Max(0, downValue/span/frame.DownScale))
			downFill = int(math.Round(ratio * float64(downCells*4)))
			if downFill < 1 {
				downFill = 1
			}
		}
		downY := downBaseline - downFill + 1
		if downY < 0 {
			downY = 0
		}
		for y := downY; y <= downBaseline; y++ {
			canvas.dot(x, y)
		}

		if upCells > 0 {
			upFill := 1
			if frame.UpScale > 0 {
				ratio := math.Min(1, math.Max(0, upValue/span/frame.UpScale))
				upFill = int(math.Round(ratio * float64(upCells*4)))
				if upFill < 1 {
					upFill = 1
				}
			}
			upY := upBaseline + upFill - 1
			if upY >= dotsHeight {
				upY = dotsHeight - 1
			}
			for y := upBaseline; y <= upY; y++ {
				canvas.dot(x, y)
			}
		}
	}
	return canvas.rows()
}

func renderTrafficLane(frame trace.Frame, width, height int, upload bool) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	canvas := newBrailleCanvas(width, height)
	if len(frame.Buckets) == 0 {
		return canvas.rows()
	}
	dotsWidth, dotsHeight := width*2, height*4
	shift := int(math.Round(frame.Shift))
	span := frame.BucketSeconds.Seconds()
	if span <= 0 {
		span = 1
	}

	for _, gridX := range frame.GridLines {
		x := int(math.Round(gridX))
		for y := 0; y < dotsHeight; y++ {
			canvas.dot(x, y)
		}
	}
	for x := 0; x < dotsWidth; x++ {
		canvas.dot(x, dotsHeight/2)
	}

	scale := frame.DownScale
	if upload {
		scale = frame.UpScale
	}
	for x := 0; x < dotsWidth; x++ {
		position := float64(x+shift) / float64(maxInt(1, dotsWidth-1)) * float64(len(frame.Buckets)-1)
		left := int(math.Floor(position))
		if left < 0 {
			left = 0
		}
		if left >= len(frame.Buckets) {
			left = len(frame.Buckets) - 1
		}
		right := left + 1
		if right >= len(frame.Buckets) {
			right = len(frame.Buckets) - 1
		}
		fraction := position - float64(left)
		leftValue := frame.Buckets[left].Download
		rightValue := frame.Buckets[right].Download
		if upload {
			leftValue = frame.Buckets[left].Upload
			rightValue = frame.Buckets[right].Upload
		}
		value := float64(leftValue) + (float64(rightValue)-float64(leftValue))*fraction
		if value <= 0 || scale <= 0 {
			continue
		}
		ratio := math.Min(1, math.Max(0, value/span/scale))
		fill := int(math.Ceil(ratio * float64(dotsHeight)))
		for y := dotsHeight - fill; y < dotsHeight; y++ {
			canvas.dot(x, y)
		}
	}
	return canvas.rows()
}

func RenderActivityBlocks(counts []int, width int) string {
	if width <= 0 {
		return ""
	}
	if len(counts) == 0 {
		return strings.Repeat(" ", width)
	}
	if len(counts) > width {
		counts = counts[len(counts)-width:]
	}
	offset := maxInt(0, width-len(counts))
	blocks := []rune(" ▁▂▃▄▅▆▇█")
	var builder strings.Builder
	for x := 0; x < width; x++ {
		if x < offset {
			builder.WriteByte(' ')
			continue
		}
		position := float64(x - offset)
		left := int(math.Floor(position))
		right := left + 1
		if right >= len(counts) {
			right = len(counts) - 1
		}
		value := float64(counts[left])
		value += (float64(counts[right]) - value) * (position - float64(left))
		index := int(math.Round(math.Min(1, value/3) * 8))
		builder.WriteRune(blocks[index])
	}
	return builder.String()
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func maxInt(first, second int) int {
	if first > second {
		return first
	}
	return second
}
