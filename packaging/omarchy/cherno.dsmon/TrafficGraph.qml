import QtQuick

// Compact version of the TUI btop-style traffic chart: download grows upward
// from the upper baseline, upload grows downward from the lower one.
Canvas {
  id: root

  property var values: []
  property real bucketSeconds: 2
  property real nowMs: 0
  property real snapshotMs: 0
  property real downScale: 0
  property real upScale: 0
  property color downLowColor: "#e0af68"
  property color downMidColor: "#f7768e"
  property color downHighColor: "#f7768e"
  property color upLowColor: "#9ece6a"
  property color upMidColor: "#449dab"
  property color upHighColor: "#7aa2f7"
  property color baselineColor: Qt.rgba(1, 1, 1, 0.12)

  onValuesChanged: requestPaint()
  onBucketSecondsChanged: requestPaint()
  onNowMsChanged: requestPaint()
  onSnapshotMsChanged: requestPaint()
  onDownScaleChanged: requestPaint()
  onUpScaleChanged: requestPaint()
  onDownLowColorChanged: requestPaint()
  onDownMidColorChanged: requestPaint()
  onDownHighColorChanged: requestPaint()
  onUpLowColorChanged: requestPaint()
  onUpMidColorChanged: requestPaint()
  onUpHighColorChanged: requestPaint()
  onBaselineColorChanged: requestPaint()
  onWidthChanged: requestPaint()
  onHeightChanged: requestPaint()
  onVisibleChanged: if (visible) requestPaint()

  function css(color, alpha) {
    return Qt.rgba(color.r, color.g, color.b, alpha)
  }

  function sampleAt(samples, position, key) {
    if (!samples || samples.length === 0) return 0
    const last = samples.length - 1
    if (last === 0 || position <= 0) return Number(samples[0][key]) || 0
    if (position >= last) return Number(samples[last][key]) || 0
    const left = Math.floor(position)
    const right = Math.min(last, left + 1)
    const t = position - left
    const a = Number(samples[left][key]) || 0
    const b = Number(samples[right][key]) || 0
    return a + (b - a) * t
  }

  function bucketPhase() {
    const span = Math.max(1, root.bucketSeconds * 1000)
    const now = Number(root.nowMs)
    if (!isFinite(now) || now <= 0) return 0
    // 与活动曲线共用同一套连续相位：快照之间允许超过 1，避免桶边界处回跳。
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

    const downBase = Math.max(1, Math.floor(h / 2) - 1)
    const upBase = Math.min(h - 2, Math.floor(h / 2))
    ctx.lineWidth = 1
    ctx.strokeStyle = css(root.baselineColor, 1)
    ctx.beginPath()
    ctx.moveTo(0, downBase + 0.5)
    ctx.lineTo(w, downBase + 0.5)
    ctx.moveTo(0, upBase + 0.5)
    ctx.lineTo(w, upBase + 0.5)
    ctx.stroke()

    const samples = root.values || []
    if (samples.length === 0) return
    const span = Math.max(0.001, root.bucketSeconds)
    const downScale = Math.max(0, root.downScale)
    const upScale = Math.max(0, root.upScale)
    const count = samples.length
    // 和活动曲线同一套时间锚：每像素一个桶、右对齐、按桶相位连续左移。
    // 不再把整个历史数组拉伸到画布宽，避免每次刷新时峰值在像素间重采样。
    const plotWidth = Math.max(1, Math.floor(w))
    const visible = Math.min(count, plotWidth)
    const shown = count > visible ? samples.slice(count - visible) : samples
    const offset = Math.max(0, plotWidth - shown.length)
    const phase = root.bucketPhase()
    const positionAt = function(x) {
      return x - offset + phase
    }
    const downYAt = function(x) {
      if (downScale <= 0) return downBase
      const position = positionAt(x)
      if (position < 0) return downBase
      const rate = root.sampleAt(shown, position, "download") / span
      return downBase - Math.min(1, Math.max(0, rate / downScale)) * downBase
    }
    const upYAt = function(x) {
      if (upScale <= 0) return upBase
      const position = positionAt(x)
      if (position < 0) return upBase
      const rate = root.sampleAt(shown, position, "upload") / span
      return upBase + Math.min(1, Math.max(0, rate / upScale)) * (h - 1 - upBase)
    }

    const downGradient = ctx.createLinearGradient(0, downBase, 0, 0)
    downGradient.addColorStop(0, css(root.downLowColor, 0.9))
    downGradient.addColorStop(0.55, css(root.downMidColor, 0.92))
    downGradient.addColorStop(1, css(root.downHighColor, 0.96))
    ctx.beginPath()
    ctx.moveTo(0, downBase)
    for (let x = 0; x < plotWidth; x++) ctx.lineTo(x, downYAt(x))
    ctx.lineTo(plotWidth, downBase)
    ctx.closePath()
    ctx.fillStyle = downGradient
    ctx.fill()

    const upGradient = ctx.createLinearGradient(0, upBase, 0, h)
    upGradient.addColorStop(0, css(root.upLowColor, 0.9))
    upGradient.addColorStop(0.55, css(root.upMidColor, 0.92))
    upGradient.addColorStop(1, css(root.upHighColor, 0.96))
    ctx.beginPath()
    ctx.moveTo(0, upBase)
    for (let x = 0; x < plotWidth; x++) ctx.lineTo(x, upYAt(x))
    ctx.lineTo(plotWidth, upBase)
    ctx.closePath()
    ctx.fillStyle = upGradient
    ctx.fill()

  }
}
