pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Hyprland
import Quickshell.Io
import Quickshell.Wayland
import qs.Commons
import qs.Commons as Commons
import qs.Ui

// DS-mon's optional activity card. It owns the status poll shared by the bar
// widget, so enabling the card does not double the number of `dsmon status`
// processes. The notch tucks itself past the right edge when idle; only its
// visible sliver participates in pointer input, so hovering that edge reveals
// the card without turning the card off.
Item {
  id: root

  property var shell: null
  property var manifest: null

  property bool statusLoaded: false
  property bool daemonOnline: false
  property bool health: false
  property bool balanceError: false
  property bool balanceLow: false
  property bool balanceHas: false
  property real balance: 0
  property int requestsToday: 0
  property int tokensToday: 0
  property real cacheHitRate: 0
  property real costToday: 0
  property bool activityCardEnabled: false
  property real lastActivityMs: 0
  property real serverNowMs: 0
  property real responseReceivedMs: 0
  property real clockMs: Date.now()
  property var activity: []
  property int activityBucketSecs: 2
  property int activityFullScale: 3
  property var traffic: []
  property real trafficBucketSecs: 2
  property real downRate: 0
  property real upRate: 0
  property real downScale: 0
  property real upScale: 0
  property color inactiveBorderColor: Commons.Color.popups.border
  readonly property var activeBorderSpec:
    Border.hyprlandActiveSpec(Commons.Color.accent, Math.max(1, compactSpace(1)))
  property real windowOpacity: 1
  property var chartColors: ({
    tempStart: "#9ece6a",
    tempMid: "#e0af68",
    tempEnd: "#f7768e",
    downloadStart: "#e0af68",
    downloadMid: "#f7768e",
    downloadEnd: "#f7768e",
    uploadStart: "#9ece6a",
    uploadMid: "#449dab",
    uploadEnd: "#7aa2f7"
  })

  property var targetScreen: null
  property bool cardOpen: false
  property bool cardHovered: false
  property bool cardVisible: false
  property bool opening: false

  readonly property int holdMs: 10000
  // 这条竖条的滑动是它“出现/收起”语义的一部分。Omarchy 的 reduceMotion 跟随
  // Hyprland animations:enabled，后者常被 CPU 渲染的用户关掉；这里不跟随，
  // 否则滑入滑出会直接变成瞬移。
  readonly property int slideDurationMs: 190
  // 主数值固定 12px；标签沿用 shell 的 caption/title 比例，当前约 9px。
  // 卡片布局按同一比例收紧，避免字号缩小后留下过多空白。
  readonly property int metricValueFontSize: 12
  readonly property int metricLabelFontSize:
    Math.max(1, Math.round(metricValueFontSize * Style.font.caption / Style.font.title))
  readonly property real compactScale: 0.75
  readonly property bool panelAvailable:
    activityCardEnabled && statusLoaded && daemonOnline
  readonly property real estimatedServerNowMs:
    serverNowMs > 0 ? serverNowMs + (clockMs - responseReceivedMs) : clockMs
  readonly property real activityAgeMs:
    lastActivityMs > 0 ? Math.max(0, estimatedServerNowMs - lastActivityMs) : Infinity
  readonly property bool shouldShow:
    panelAvailable && lastActivityMs > 0 && activityAgeMs < holdMs
  readonly property bool revealed:
    cardOpen && (shouldShow || cardHovered)
  readonly property int barThickness:
    shell && shell.bar && targetScreen
      ? Math.max(Style.bar.sizeHorizontal, shell.bar.barSizeFor(String(targetScreen.name)))
      : Style.bar.sizeHorizontal
  readonly property real topInset: barThickness + compactSpace(8)
  readonly property real cardWidth: compactSpace(88)
  // 缩进后的悬停残边仍保持用户要求的 5px，不随卡片尺寸缩放。
  readonly property real peekWidth: 5
  readonly property real contentInsetX: compactSpace(12)
  readonly property real contentInsetY: compactSpace(22)
  readonly property real rowHeight: compactSpace(38)
  readonly property real rowSpacing: compactSpace(6)
  readonly property real chartTitleHeight: compactSpace(16)
  readonly property real trafficChartHeight: compactSpace(70)
  readonly property real activityChartHeight: Math.max(1, Math.round(trafficChartHeight / 2))
  // 左右两侧都跟随系统窗口圆角；右侧是贴屏幕边缘的凹角，0 时退化为直角。
  readonly property real notchFlare: Math.max(0, Style.cornerRadius)
  readonly property real notchCorner: Math.max(0, Style.cornerRadius)
  readonly property real dividerInset: compactSpace(10)
  readonly property real cardHeight:
    contentInsetY * 2 + rowHeight * 5 + rowSpacing * 6
      + chartTitleHeight * 2 + activityChartHeight + trafficChartHeight
  readonly property real cardRevealTarget:
    opening ? 0 : (cardOpen ? (revealed ? cardWidth : peekWidth) : 0)
  property real cardRevealWidth: cardRevealTarget

  function compactSpace(px) {
    return Math.max(1, Math.round(Style.space(px) * compactScale))
  }

  function focusedScreen() {
    const monitor = Hyprland.focusedMonitor
    const name = monitor ? String(monitor.name || "") : ""
    const screens = Quickshell.screens
    for (let index = 0; index < screens.length; index++) {
      if (String(screens[index].name) === name) return screens[index]
    }
    return screens.length > 0 ? screens[0] : null
  }

  onPanelAvailableChanged: {
    if (panelAvailable) {
      closeTimer.stop()
      if (cardOpen) return
      const screen = focusedScreen()
      if (screen === null) {
        cardVisible = false
        return
      }
      targetScreen = screen
      // 首次映射时先以 0 可见宽度放在屏幕外，下一拍再滑入，避免层表面创建
      // 的第一帧闪在终点。
      if (!cardVisible) {
        opening = true
        cardVisible = true
      }
      cardOpen = true
      if (opening) Qt.callLater(function() {
        if (root.panelAvailable && root.cardOpen) root.opening = false
      })
      return
    }
    closeTimer.stop()
    opening = false
    cardOpen = false
    cardHovered = false
    if (!cardVisible) return
    closeTimer.restart()
  }

  function refresh() {
    if (!statusProc.running) statusProc.running = true
  }

  function boundedNumber(value, minimum, maximum, fallback) {
    const number = Number(value)
    if (!isFinite(number)) return fallback
    return Math.min(maximum, Math.max(minimum, number))
  }

  function sanitizeActivity(value) {
    if (!Array.isArray(value)) return []
    const start = Math.max(0, value.length - 200)
    const result = []
    for (let index = start; index < value.length; index++) {
      result.push(Math.floor(boundedNumber(value[index], 0, 1000000, 0)))
    }
    return result
  }

  function sanitizeTraffic(value) {
    if (!Array.isArray(value)) return []
    const start = Math.max(0, value.length - 200)
    const result = []
    for (let index = start; index < value.length; index++) {
      const bucket = value[index]
      if (!bucket || typeof bucket !== "object") {
        result.push({ upload: 0, download: 0 })
        continue
      }
      result.push({
        upload: Math.floor(boundedNumber(bucket.upload, 0, 1000000000000, 0)),
        download: Math.floor(boundedNumber(bucket.download, 0, 1000000000000, 0))
      })
    }
    return result
  }

  function applyStatus(text) {
    let value
    try {
      value = JSON.parse(String(text || ""))
    } catch (error) {
      setOffline()
      return
    }
    if (!value || typeof value.online !== "boolean") {
      setOffline()
      return
    }

    const received = Date.now()
    statusLoaded = true
    daemonOnline = value.online === true
    health = value.health === true
    balanceError = value.balance_error === true
    balanceLow = value.balance_low === true
    balanceHas = value.balance_has === true
    balance = boundedNumber(value.balance, 0, 1e12, 0)
    requestsToday = Math.floor(boundedNumber(value.requests_today, 0, 1e9, 0))
    tokensToday = Math.floor(boundedNumber(value.tokens_today, 0, 1e12, 0))
    cacheHitRate = boundedNumber(value.cache_hit_rate, 0, 100, 0)
    costToday = boundedNumber(value.cost_today, 0, 1e12, 0)
    activityCardEnabled = value.activity_card_enabled === true
    lastActivityMs = boundedNumber(value.last_activity_unix_ms, 0, 9e15, 0)
    serverNowMs = boundedNumber(value.server_now_unix_ms, 0, 9e15, 0)
    responseReceivedMs = received
    clockMs = received
    activity = sanitizeActivity(value.activity)
    activityBucketSecs = Math.max(1, Math.floor(boundedNumber(value.activity_bucket_seconds, 1, 60, 2)))
    activityFullScale = Math.max(1, Math.floor(boundedNumber(value.activity_full_scale, 1, 1000, 3)))
    traffic = sanitizeTraffic(value.traffic)
    trafficBucketSecs = boundedNumber(value.traffic_bucket_seconds, 0.1, 60, 2)
    downRate = boundedNumber(value.down_rate, 0, 1e12, 0)
    upRate = boundedNumber(value.up_rate, 0, 1e12, 0)
    downScale = boundedNumber(value.down_scale, 0, 1e12, 0)
    upScale = boundedNumber(value.up_scale, 0, 1e12, 0)

  }

  function setOffline() {
    statusLoaded = true
    daemonOnline = false
    health = false
    balanceError = false
    balanceLow = false
    lastActivityMs = 0
    serverNowMs = 0
    responseReceivedMs = Date.now()
    clockMs = responseReceivedMs
  }

  function formatCount(value) {
    const number = Math.max(0, Number(value) || 0)
    if (number >= 1e9) return trimNumber(number / 1e9) + "B"
    if (number >= 1e6) return trimNumber(number / 1e6) + "M"
    if (number >= 1e3) return trimNumber(number / 1e3) + "K"
    return String(Math.round(number))
  }

  function trimNumber(value) {
    if (value >= 10) return String(Math.round(value))
    return String(Math.round(value * 10) / 10)
  }

  function formatBalance() {
    if (!balanceHas) return "--"
    return "¥" + Commons.I18n.formatNumber(balance, 2)
  }

  function formatMoney(value) {
    return "¥" + Commons.I18n.formatNumber(Math.max(0, Number(value) || 0), 2)
  }

  function formatRate(value) {
    let rate = Math.max(0, Number(value) || 0)
    if (rate >= 1024 * 1024) return trimNumber(rate / (1024 * 1024)) + "M/s"
    if (rate >= 1024) return trimNumber(rate / 1024) + "K/s"
    return Math.round(rate) + "B/s"
  }

  function loadChartColors() {
    const colors = {
      tempStart: "#9ece6a",
      tempMid: "#e0af68",
      tempEnd: "#f7768e",
      downloadStart: "#e0af68",
      downloadMid: "#f7768e",
      downloadEnd: "#f7768e",
      uploadStart: "#9ece6a",
      uploadMid: "#449dab",
      uploadEnd: "#7aa2f7"
    }
    const raw = String(btopTheme.text() || "")
    const pattern = /theme\[([A-Za-z0-9_]+)\]\s*=\s*"?(#[0-9A-Fa-f]{6})/g
    let match
    while ((match = pattern.exec(raw)) !== null) {
      if (Object.prototype.hasOwnProperty.call(colors, match[1])) colors[match[1]] = match[2]
    }
    chartColors = colors
  }

  function parseHyprlandColor(raw) {
    try {
      const json = JSON.parse(String(raw || "{}"))
      const token = String(json.gradient || json.color || "").trim().split(/\s+/)[0]
      let match = token.match(/^#?([0-9A-Fa-f]{8})$/)
      if (match) return "#" + match[1]
      match = token.match(/^#?([0-9A-Fa-f]{6})$/)
      if (match) return "#ff" + match[1]
      match = token.match(/^rgba\(([0-9A-Fa-f]{6})([0-9A-Fa-f]{2})\)$/)
      if (match) return "#" + match[2] + match[1]
      match = token.match(/^rgb\(([0-9A-Fa-f]{6})\)$/)
      if (match) return "#ff" + match[1]
    } catch (error) {
      // hyprctl 暂不可用时保留上一次读到的边框色。
    }
    return root.inactiveBorderColor
  }

  function applyWindowOpacity(raw) {
    try {
      const value = Number(JSON.parse(String(raw || "{}")).float)
      if (isFinite(value)) root.windowOpacity = Math.min(1, Math.max(0, value))
    } catch (error) {
      // hyprctl 暂不可用时保留上一次读到的窗口不透明度。
    }
  }

  function refreshSystemAppearance() {
    if (!inactiveBorderProc.running) inactiveBorderProc.running = true
    if (!windowOpacityProc.running) windowOpacityProc.running = true
  }

  Component.onCompleted: {
    clockMs = Date.now()
    loadChartColors()
    refreshSystemAppearance()
  }

  Behavior on cardRevealWidth {
    NumberAnimation { duration: root.slideDurationMs; easing.type: Easing.OutCubic }
  }

  Timer {
    id: closeTimer
    interval: root.slideDurationMs + 32
    repeat: false
    onTriggered: {
      if (root.panelAvailable || root.cardOpen) return
      root.cardVisible = false
    }
  }

  Timer {
    interval: 2000
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.refresh()
  }

  Timer {
    interval: 250
    running: root.statusLoaded && root.daemonOnline
    repeat: true
    onTriggered: root.clockMs = Date.now()
  }

  Connections {
    target: Quickshell
    function onScreensChanged() {
      if (root.cardOpen && Quickshell.screens.indexOf(root.targetScreen) < 0)
        root.targetScreen = root.focusedScreen()
    }
  }

  Connections {
    target: Hyprland
    function onRawEvent(event) {
      if (event && String(event.name) === "configreloaded") root.refreshSystemAppearance()
    }
  }

  FileView {
    id: btopTheme
    path: Quickshell.env("HOME") + "/.local/state/omarchy/current/theme/btop.theme"
    watchChanges: true
    printErrors: false
    onFileChanged: root.loadChartColors()
    onLoaded: root.loadChartColors()
  }

  Process {
    id: statusProc
    command: ["dsmon", "status", "--json"]
    onExited: function(exitCode) {
      if (exitCode !== 0) root.setOffline()
    }
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: root.applyStatus(text)
    }
  }

  Process {
    id: inactiveBorderProc
    command: ["hyprctl", "-j", "getoption", "general:col.inactive_border"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: root.inactiveBorderColor = root.parseHyprlandColor(text)
    }
  }

  Process {
    id: windowOpacityProc
    command: ["hyprctl", "-j", "getoption", "decoration:inactive_opacity"]
    stdout: StdioCollector {
      waitForEnd: true
      onStreamFinished: root.applyWindowOpacity(text)
    }
  }

  PanelWindow {
    id: overlay
    screen: root.targetScreen
    visible: root.cardVisible && root.targetScreen !== null
    anchors { top: true; right: true; bottom: true; left: true }
    color: "transparent"
    exclusionMode: ExclusionMode.Ignore
    WlrLayershell.namespace: "cherno-dsmon-activity"
    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.keyboardFocus: WlrKeyboardFocus.None
    // The visible card is also the hover target. When tucked, only the 5px
    // sliver still lies inside the screen, so that is all the input region
    // exposes; the rest of the screen remains untouched.
    mask: Region { item: card }

    NotchSurface {
      id: card
      width: root.cardWidth
      height: root.cardHeight
      x: overlay.width - root.cardRevealWidth
      y: root.topInset
      opacity: root.windowOpacity
      fillColor: Commons.Color.popups.background
      borderColor: root.inactiveBorderColor
      borderSpec: root.cardHovered ? root.activeBorderSpec : null
      borderWidth: Math.max(1, compactSpace(1))
      flare: root.notchFlare
      corner: root.notchCorner
      Behavior on y {
        NumberAnimation { duration: root.slideDurationMs; easing.type: Easing.OutCubic }
      }

      HoverHandler {
        onHoveredChanged: root.cardHovered = hovered
      }

      Item {
        anchors.fill: parent
        anchors.leftMargin: root.contentInsetX
        anchors.rightMargin: root.contentInsetX
        anchors.topMargin: root.contentInsetY
        anchors.bottomMargin: root.contentInsetY

        Column {
          width: parent.width
          spacing: 0

          Item {
            width: parent.width
            height: root.chartTitleHeight + root.activityChartHeight

            Text {
              width: parent.width
              height: root.chartTitleHeight
              text: "REQUESTS"
              textFormat: Text.PlainText
              color: Util.alpha(Commons.Color.popups.text, 0.62)
              font.family: Style.font.family
              font.pixelSize: root.metricLabelFontSize
              font.letterSpacing: 0.7
            }
            ActivityGraph {
              y: root.chartTitleHeight
              width: parent.width
              height: root.activityChartHeight
              values: root.activity
              fullScale: root.activityFullScale
              bucketSeconds: root.activityBucketSecs
              nowMs: root.estimatedServerNowMs
              snapshotMs: root.serverNowMs
              lowColor: root.chartColors.tempStart
              midColor: root.chartColors.tempMid
              highColor: root.chartColors.tempEnd
              baselineColor: Util.alpha(Commons.Color.popups.text, 0.16)
            }
          }

          Item {
            width: parent.width
            height: root.rowSpacing
          }

          NotchMetric {
            width: parent.width
            labelText: "BALANCE"
            valueText: root.formatBalance()
            valueColor: root.balanceSeverityColor
          }

          NotchDivider {
            width: parent.width
          }

          NotchMetric {
            width: parent.width
            labelText: "TOKENS"
            valueText: root.formatCount(root.tokensToday)
          }

          NotchDivider {
            width: parent.width
          }

          NotchMetric {
            width: parent.width
            labelText: "REQUESTS"
            valueText: root.formatCount(root.requestsToday)
          }

          NotchDivider {
            width: parent.width
          }

          NotchMetric {
            width: parent.width
            labelText: "HIT RATE"
            valueText: Commons.I18n.formatNumber(root.cacheHitRate, 1) + "%"
          }

          NotchDivider {
            width: parent.width
          }

          NotchMetric {
            width: parent.width
            labelText: "TODAY COST"
            valueText: root.formatMoney(root.costToday)
          }

          Item {
            width: parent.width
            height: root.rowSpacing
          }

          Item {
            width: parent.width
            height: root.chartTitleHeight + root.trafficChartHeight

            Text {
              width: parent.width
              height: root.chartTitleHeight
              text: "TRAFFIC"
              textFormat: Text.PlainText
              color: Util.alpha(Commons.Color.popups.text, 0.62)
              font.family: Style.font.family
              font.pixelSize: root.metricLabelFontSize
              font.letterSpacing: 0.7
            }
            Item {
              y: root.chartTitleHeight
              width: parent.width
              height: root.trafficChartHeight

              TrafficGraph {
                anchors.fill: parent
                values: root.traffic
                bucketSeconds: root.trafficBucketSecs
                nowMs: root.estimatedServerNowMs
                snapshotMs: root.serverNowMs
                downScale: root.downScale
                upScale: root.upScale
                downLowColor: root.chartColors.downloadStart
                downMidColor: root.chartColors.downloadMid
                downHighColor: root.chartColors.downloadEnd
                upLowColor: root.chartColors.uploadStart
                upMidColor: root.chartColors.uploadMid
                upHighColor: root.chartColors.uploadEnd
                baselineColor: Util.alpha(Commons.Color.popups.text, 0.16)
              }

              TrafficLaneLabel {
                x: root.compactSpace(2)
                y: root.compactSpace(2)
                text: "↓ " + root.formatRate(root.downRate)
                labelColor: root.chartColors.downloadMid
              }
              TrafficLaneLabel {
                x: root.compactSpace(2)
                y: parent.height - height - root.compactSpace(2)
                text: "↑ " + root.formatRate(root.upRate)
                labelColor: root.chartColors.uploadEnd
              }
            }
          }
        }
      }
    }
  }

  readonly property color balanceSeverityColor: root.balanceError
    ? root.chartColors.tempEnd
    : (root.balanceLow ? root.chartColors.tempMid : Commons.Color.popups.text)

  component NotchDivider: Item {
    height: root.rowSpacing

    Rectangle {
      x: root.dividerInset
      y: Math.floor((parent.height - 2) / 2)
      width: parent.width - root.dividerInset * 2
      height: 1
      color: Util.alpha(Commons.Color.popups.text, 0.11)
    }
    Rectangle {
      x: root.dividerInset
      y: Math.floor(parent.height / 2)
      width: parent.width - root.dividerInset * 2
      height: 1
      color: Qt.rgba(0, 0, 0, 0.32)
    }
  }

  component NotchMetric: Item {
    property string labelText: ""
    property string valueText: ""
    property color valueColor: Commons.Color.popups.text
    height: root.rowHeight

    Column {
      anchors.left: parent.left
      anchors.right: parent.right
      anchors.verticalCenter: parent.verticalCenter
      spacing: root.compactSpace(1)

      Text {
        width: parent.width
        height: root.compactSpace(12)
        text: parent.parent.labelText
        textFormat: Text.PlainText
        horizontalAlignment: Text.AlignLeft
        verticalAlignment: Text.AlignVCenter
        color: Util.alpha(Commons.Color.popups.text, 0.5)
        font.family: Style.font.family
        font.pixelSize: root.metricLabelFontSize
        font.letterSpacing: 0.45
      }
      Text {
        width: parent.width
        height: root.compactSpace(20)
        text: parent.parent.valueText
        textFormat: Text.PlainText
        horizontalAlignment: Text.AlignHCenter
        verticalAlignment: Text.AlignVCenter
        wrapMode: Text.NoWrap
        color: parent.parent.valueColor
        font.family: Style.font.family
        font.pixelSize: root.metricValueFontSize
        font.weight: Font.Normal
        font.features: ({ "tnum": 1 })
        fontSizeMode: Text.Fit
        minimumPixelSize: root.metricLabelFontSize
      }
    }
  }

  component TrafficLaneLabel: Rectangle {
    property string text: ""
    property color labelColor: Commons.Color.popups.text
    width: label.implicitWidth + root.compactSpace(4)
    height: label.implicitHeight + root.compactSpace(1)
    radius: root.compactSpace(2)
    color: Qt.rgba(0, 0, 0, 0.42)

    Text {
      id: label
      anchors.centerIn: parent
      text: parent.text
      textFormat: Text.PlainText
      color: parent.labelColor
      font.family: Style.font.family
      font.pixelSize: root.metricLabelFontSize
      font.features: ({ "tnum": 1 })
    }
  }
}
