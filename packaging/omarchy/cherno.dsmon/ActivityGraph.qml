import QtQuick

// Compact version of the TUI activity area chart: one bucket per sample,
// newest data on the right, Catmull-Rom interpolation, and a vertical gradient
// from the low colour at the baseline to the high colour at the peaks.
Canvas {
  id: root

  property var values: []
  property int fullScale: 3
  property real bucketSeconds: 2
  property real nowMs: 0
  property real snapshotMs: 0
  property color lowColor: "#9ece6a"
  property color midColor: "#e0af68"
  property color highColor: "#f7768e"
  property color baselineColor: Qt.rgba(1, 1, 1, 0.12)

  onValuesChanged: requestPaint()
  onFullScaleChanged: requestPaint()
  onBucketSecondsChanged: requestPaint()
  onNowMsChanged: requestPaint()
  onSnapshotMsChanged: requestPaint()
  onLowColorChanged: requestPaint()
  onMidColorChanged: requestPaint()
  onHighColorChanged: requestPaint()
  onBaselineColorChanged: requestPaint()
  onWidthChanged: requestPaint()
  onHeightChanged: requestPaint()
  onVisibleChanged: if (visible) requestPaint()

  function css(color, alpha) {
    return Qt.rgba(color.r, color.g, color.b, alpha)
  }

  function valueAt(samples, position) {
    if (!samples || samples.length === 0) return 0
    const last = samples.length - 1
    if (last === 0 || position <= 0) return Number(samples[0]) || 0
    if (position >= last) return Number(samples[last]) || 0
    const left = Math.floor(position)
    const t = position - left
    const p0 = Number(samples[Math.max(0, left - 1)]) || 0
    const p1 = Number(samples[left]) || 0
    const p2 = Number(samples[Math.min(last, left + 1)]) || 0
    const p3 = Number(samples[Math.min(last, left + 2)]) || 0
    const value = 0.5 * (2 * p1 +
      (-p0 + p2) * t +
      (2 * p0 - 5 * p1 + 4 * p2 - p3) * t * t +
      (-p0 + 3 * p1 - 3 * p2 + p3) * t * t * t)
    return Math.max(0, value)
  }

  function bucketPhase() {
    const span = Math.max(1, root.bucketSeconds * 1000)
    const now = Number(root.nowMs)
    if (!isFinite(now) || now <= 0) return 0
    // 相位从承载数据的快照时刻开始累计，允许超过 1。快照之间跨过桶边界时，
    // 曲线继续左移，新快照到达后数组左移一格、相位回绕，两者正好抵消。
    const anchor = Number(root.snapshotMs) > 0 ? Number(root.snapshotMs) : now
    const anchorBucket = Math.floor(anchor / span)
    return Math.max(0, now / span - anchorBucket)
  }

  onPaint: {
    const ctx = getContext("2d")
    const w = width
    const h = height
    ctx.clearRect(0, 0, w, h)
    if (w <= 0 || h <= 0) return

    const baselineY = h - 1
    ctx.lineWidth = 1
    ctx.strokeStyle = css(root.baselineColor, 1)
    ctx.beginPath()
    ctx.moveTo(0, baselineY + 0.5)
    ctx.lineTo(w, baselineY + 0.5)
    ctx.stroke()

    const samples = root.values || []
    if (samples.length === 0 || root.fullScale <= 0) return

    const maxValue = Math.max(1, root.fullScale)
    const usableHeight = Math.max(1, h - 1)
    const count = samples.length
    // 一个像素固定对应一个桶；只显示当前宽度装得下的最近一段。此前把 160 个桶
    // 拉伸进 64px，每次新桶进来都会重采样整条历史，峰值高度就在像素网格间跳动。
    const plotWidth = Math.max(1, Math.floor(w))
    const visible = Math.min(count, plotWidth)
    const shown = count > visible ? samples.slice(count - visible) : samples
    const offset = Math.max(0, plotWidth - shown.length)
    const phase = root.bucketPhase()
    const positionAt = function(x) {
      return x - offset + phase
    }
    const yAt = function(x) {
      const position = positionAt(x)
      if (position < 0) return baselineY
      const value = root.valueAt(shown, position)
      return baselineY - Math.min(1, value / maxValue) * usableHeight
    }

    const gradient = ctx.createLinearGradient(0, baselineY, 0, 0)
    gradient.addColorStop(0, css(root.lowColor, 0.92))
    gradient.addColorStop(0.52, css(root.midColor, 0.92))
    gradient.addColorStop(1, css(root.highColor, 0.96))

    ctx.beginPath()
    ctx.moveTo(0, baselineY)
    for (let x = 0; x < plotWidth; x++) ctx.lineTo(x, yAt(x))
    ctx.lineTo(plotWidth, baselineY)
    ctx.closePath()
    ctx.fillStyle = gradient
    ctx.fill()

  }
}
