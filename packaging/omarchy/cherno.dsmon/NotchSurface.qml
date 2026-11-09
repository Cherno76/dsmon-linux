import QtQuick

// 贴右边框的刘海底板：屏幕边一侧不留缝，用凹角让形状看起来从边框里长出来。
// 轮廓和 Mac 版 SideNotchGeometry 的右侧变体一致。
Canvas {
  id: root

  property color fillColor: "#151515"
  property color borderColor: "#303030"
  property real borderWidth: 1
  property real flare: 7
  property real corner: 10

  onFillColorChanged: requestPaint()
  onBorderColorChanged: requestPaint()
  onBorderWidthChanged: requestPaint()
  onFlareChanged: requestPaint()
  onCornerChanged: requestPaint()
  onWidthChanged: requestPaint()
  onHeightChanged: requestPaint()
  onVisibleChanged: if (visible) requestPaint()

  onPaint: {
    const ctx = getContext("2d")
    const w = width
    const h = height
    ctx.clearRect(0, 0, w, h)
    if (w <= 0 || h <= 0) return

    const f = Math.min(root.flare, w, h / 2)
    const c = Math.max(0, Math.min(root.corner, w - f, h / 2 - f))

    ctx.beginPath()
    ctx.moveTo(w, 0)
    ctx.quadraticCurveTo(w, f, w - f, f)
    ctx.lineTo(c, f)
    ctx.quadraticCurveTo(0, f, 0, f + c)
    ctx.lineTo(0, h - f - c)
    ctx.quadraticCurveTo(0, h - f, c, h - f)
    ctx.lineTo(w - f, h - f)
    ctx.quadraticCurveTo(w, h - f, w, h)
    ctx.closePath()

    ctx.fillStyle = root.fillColor
    ctx.fill()
    if (root.borderWidth > 0) {
      // 右缘贴着屏幕边，不再描一条竖线；填充仍然闭合到 w，只是让底色和屏幕
      // 黑边直接相接。描边路径从右上凹角走到右下凹角，不 closePath。
      ctx.beginPath()
      ctx.moveTo(w, 0)
      ctx.quadraticCurveTo(w, f, w - f, f)
      ctx.lineTo(c, f)
      ctx.quadraticCurveTo(0, f, 0, f + c)
      ctx.lineTo(0, h - f - c)
      ctx.quadraticCurveTo(0, h - f, c, h - f)
      ctx.lineTo(w - f, h - f)
      ctx.quadraticCurveTo(w, h - f, w, h)
      ctx.lineWidth = root.borderWidth
      ctx.strokeStyle = root.borderColor
      ctx.stroke()
    }
  }
}
