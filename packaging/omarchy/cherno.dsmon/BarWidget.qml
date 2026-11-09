pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Commons as Commons
import qs.Ui

BarWidget {
  id: root
  moduleName: "cherno.dsmon"

  property bool statusLoaded: false
  property bool daemonOnline: false
  property bool health: false
  property bool balanceError: false
  property bool balanceLow: false
  property bool balanceHas: false
  property real balance: 0
  property int requestsToday: 0
  property real costToday: 0
  property real lastActivityMs: 0
  property real serverNowMs: 0
  property real responseReceivedMs: 0

  property real animationClockMs: Date.now()
  property real slotPosition: 2
  property bool sliding: false
  property real slideFrom: 2
  property real slideTo: 2
  property real slideStartMs: 0
  property real slideDurationMs: 0
  property real needle: 0
  property bool needleRising: false
  property real needleRiseFrom: 0
  property real needleRiseStartMs: 0
  property real acknowledgedActivityMs: 0
  property real needleHoldUntilMs: 0
  property real activityGlow: 0
  property real idleLift: 0
  property bool needleNudging: false
  property real idleNudgeNextAtMs: 0
  property real idleNudgeStartMs: 0

  readonly property real estimatedServerNowMs:
    serverNowMs > 0 ? serverNowMs + (animationClockMs - responseReceivedMs) : animationClockMs
  readonly property real activityAgeMs:
    lastActivityMs > 0 ? Math.max(0, estimatedServerNowMs - lastActivityMs) : Infinity
  readonly property bool alarm:
    statusLoaded && (!daemonOnline || !health || balanceError || balanceLow)
  readonly property int targetSlot:
    !statusLoaded ? 2 : (alarm ? 0 : 2)
  readonly property bool alertBlinkOn:
    Style.reduceMotion || (animationClockMs % 600) < 300
  readonly property real sirenOpacity:
    alarm && !Style.reduceMotion ? (alertBlinkOn ? 1 : 0.35) : 1
  readonly property color templateColor:
    root.bar ? root.bar.barForeground : Commons.Color.foreground
  readonly property color barBackground:
    root.bar ? root.bar.background : Commons.Color.background
  readonly property bool darkBar:
    0.2126 * barBackground.r + 0.7152 * barBackground.g + 0.0722 * barBackground.b < 0.5
  readonly property color glowColor: darkBar ? "#ff9445" : "#c44a00"
  readonly property real displayNeedle: Math.max(needle, idleLift)
  readonly property bool iconAnimating: sliding || needleRising || needleNudging
  readonly property real needleRotation: 195 + displayNeedle * 240

  Commons.Translations {
    id: strings
    directory: Qt.resolvedUrl("i18n")
  }

  function openDashboard() {
    Quickshell.execDetached([
      "omarchy-launch-or-focus-tui",
      "--app-id=dsmon-dashboard",
      "dsmon",
      "tui"
    ])
  }

  function refresh() {
    if (!statusProc.running) statusProc.running = true
  }

  function boundedNumber(value, minimum, maximum, fallback) {
    const number = Number(value)
    if (!isFinite(number)) return fallback
    return Math.min(maximum, Math.max(minimum, number))
  }

  function applyStatus(text) {
    var value
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
    daemonOnline = value.online
    health = value.health === true
    balanceError = value.balance_error === true
    balanceLow = value.balance_low === true
    balanceHas = value.balance_has === true
    balance = boundedNumber(value.balance, 0, 1e12, 0)
    requestsToday = Math.floor(boundedNumber(value.requests_today, 0, 1e9, 0))
    costToday = boundedNumber(value.cost_today, 0, 1e12, 0)
    const legacyLastActivityNs = boundedNumber(value.last_activity_unix_nano, 0, 9e18, 0)
    const legacyServerNowNs = boundedNumber(value.server_now_unix_nano, 0, 9e18, 0)
    lastActivityMs = value.last_activity_unix_ms !== undefined
      ? boundedNumber(value.last_activity_unix_ms, 0, 9e15, 0)
      : legacyLastActivityNs / 1e6
    serverNowMs = value.server_now_unix_ms !== undefined
      ? boundedNumber(value.server_now_unix_ms, 0, 9e15, 0)
      : legacyServerNowNs / 1e6
    responseReceivedMs = received
    tick(received)
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
    tick(Date.now())
  }

  function smoothstep(value) {
    const t = Math.min(1, Math.max(0, value))
    return t * t * (3 - 2 * t)
  }

  function tick(now) {
    animationClockMs = now
    advanceNeedle(now)
    advanceSlide(now)
    advanceIdleNudge(now)
  }

  function advanceNeedle(now) {
    if (lastActivityMs !== acknowledgedActivityMs) {
      acknowledgedActivityMs = lastActivityMs
      if (lastActivityMs <= 0) {
        needle = 0
        needleRising = false
        needleHoldUntilMs = 0
        activityGlow = 0
      } else {
        needleHoldUntilMs = lastActivityMs + 10000
        const ageAtDetection = estimatedServerNowMs - lastActivityMs
        if (ageAtDetection >= 10000) {
          needle = 0
          needleRising = false
        } else {
          needleRiseFrom = needle
          needleRiseStartMs = now
          needleRising = true
        }
      }
    }
    if (targetSlot !== 2) {
      needle = 0
      needleRising = false
      activityGlow = 0
      return
    }
    if (needleRising) {
      const elapsed = now - needleRiseStartMs
      if (elapsed < 400) {
        needle = needleRiseFrom + (1 - needleRiseFrom) * smoothstep(elapsed / 400)
        activityGlow = needle
        return
      }
      needle = 1
      needleRising = false
    }
    if (lastActivityMs <= 0 || now >= needleHoldUntilMs) {
      needle = 0
      activityGlow = 0
      return
    }
    const fallStart = needleRiseStartMs + 400
    const span = needleHoldUntilMs - fallStart
    needle = span > 50
      ? Math.min(1, Math.max(0, 1 - (now - fallStart) / span))
      : 0
    activityGlow = needle
  }

  function advanceIdleNudge(now) {
    const idleForNudge = targetSlot === 2
      && !needleRising
      && !sliding
      && needle < 1e-9
    if (!idleForNudge || Style.reduceMotion) {
      resetIdleNudge()
      return
    }
    if (needleNudging) {
      const elapsed = now - idleNudgeStartMs
      if (elapsed >= 0 && elapsed < 1120) {
        idleLift = shapedIdleLift(elapsed)
        return
      }
      needleNudging = false
      idleLift = 0
    }
    if (idleNudgeNextAtMs > 0) {
      if (now < idleNudgeNextAtMs) return
      idleNudgeNextAtMs = 0
      idleNudgeStartMs = now
      needleNudging = true
      idleLift = 0
      return
    }
    idleNudgeNextAtMs = now + 25000 + Math.random() * 20000
  }

  function resetIdleNudge() {
    idleNudgeNextAtMs = 0
    idleNudgeStartMs = 0
    needleNudging = false
    idleLift = 0
  }

  function shapedIdleLift(elapsed) {
    const rise = 230
    const fall = 250
    const gap = 160
    const firstFallEnd = rise + fall
    const secondRiseStart = firstFallEnd + gap
    const secondRiseEnd = secondRiseStart + rise
    const maxLift = 16 / 240
    if (elapsed < rise) return maxLift * smoothstep(elapsed / rise)
    if (elapsed < firstFallEnd) return maxLift * (1 - smoothstep((elapsed - rise) / fall))
    if (elapsed < secondRiseStart) return 0
    if (elapsed < secondRiseEnd) {
      return maxLift * smoothstep((elapsed - secondRiseStart) / rise)
    }
    return maxLift * (1 - smoothstep((elapsed - secondRiseEnd) / fall))
  }

  function advanceSlide(now) {
    const target = targetSlot
    if (!sliding && Math.abs(slotPosition - target) < 1e-6) return
    if (!sliding || slideTo !== target) {
      slideFrom = slotPosition
      slideTo = target
      slideStartMs = now
      slideDurationMs = 450 * Math.abs(target - slotPosition)
      if (slideDurationMs <= 1) {
        slotPosition = target
        sliding = false
        return
      }
      sliding = true
    }
    const fraction = (now - slideStartMs) / slideDurationMs
    if (fraction >= 1) {
      slotPosition = slideTo
      sliding = false
      return
    }
    slotPosition = slideFrom + (slideTo - slideFrom) * smoothstep(fraction)
  }

  readonly property string tooltipText: {
    if (!statusLoaded) return strings.t("status.loading")
    if (!daemonOnline) return strings.t("status.offline")
    if (!health) return strings.t("status.error")
    if (balanceError || balanceLow) return strings.t("status.balance")
    if (activityAgeMs < 10000) return strings.t("status.active")
    return strings.t("status.idle")
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  Component.onCompleted: tick(Date.now())

  Timer {
    interval: 2000
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.refresh()
  }

  Timer {
    interval: root.iconAnimating ? 33 : 100
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: root.tick(Date.now())
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

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    slotSize: Style.bar.statusSlot
    tooltipText: root.tooltipText
    onPressed: root.openDashboard()
    iconComponent: Component {
      Item {
        width: 24
        height: 24

        Item {
          width: 24
          height: 24
          clip: true

          Image {
            x: (0 - root.slotPosition) * 24
            width: 24
            height: 24
            source: Qt.resolvedUrl("icons/menu_icon_siren.png")
            fillMode: Image.PreserveAspectFit
            sourceSize.width: Math.round(width * Screen.devicePixelRatio)
            sourceSize.height: Math.round(height * Screen.devicePixelRatio)
            opacity: root.sirenOpacity
          }

          Image {
            x: (1 - root.slotPosition) * 24
            width: 24
            height: 24
            source: Qt.resolvedUrl("icons/menu_icon_warning.png")
            fillMode: Image.PreserveAspectFit
            sourceSize.width: Math.round(width * Screen.devicePixelRatio)
            sourceSize.height: Math.round(height * Screen.devicePixelRatio)
          }

          Item {
            x: (2 - root.slotPosition) * 24
            width: 24
            height: 24

            Canvas {
              anchors.fill: parent
              property real glow: root.activityGlow
              property color glowInk: root.glowColor
              visible: glow > 0
              onGlowChanged: requestPaint()
              onGlowInkChanged: requestPaint()
              onVisibleChanged: if (visible) requestPaint()
              onWidthChanged: requestPaint()
              onHeightChanged: requestPaint()
              onPaint: {
                const ctx = getContext("2d")
                ctx.clearRect(0, 0, width, height)
                const raw = Math.min(1, Math.max(0, glow))
                if (raw <= 0) return
                const amount = Math.pow(raw, 0.68)
                const unit = width / 24
                const cx = 12 * unit
                const cy = 14 * unit
                const radius = 10 * unit
                const color = glowInk

                // 与指针共用 (12,14) 枢轴；从左侧 150° 扫到右侧 390°，共 240°。
                ctx.beginPath()
                for (let step = 0; step <= 60; step++) {
                  const angle = Math.PI * (150 + step * 4) / 180
                  const x = cx + radius * Math.cos(angle)
                  const y = cy + radius * Math.sin(angle)
                  if (step === 0) ctx.moveTo(x, y)
                  else ctx.lineTo(x, y)
                }
                ctx.closePath()

                const gradient = ctx.createRadialGradient(
                  cx, cy, 0, cx, cy, radius * (0.36 + 0.86 * amount))
                gradient.addColorStop(0, Qt.rgba(color.r, color.g, color.b, 0.16 + 0.44 * amount))
                gradient.addColorStop(0.62, Qt.rgba(color.r, color.g, color.b, 0.05 + 0.29 * amount))
                gradient.addColorStop(1, Qt.rgba(color.r, color.g, color.b, 0.04))
                ctx.fillStyle = gradient
                ctx.fill()
              }
            }

            TintedImage {
              anchors.fill: parent
              source: Qt.resolvedUrl("icons/menu_icon_gauge.png")
              tint: root.templateColor
            }

            TintedImage {
              anchors.fill: parent
              source: Qt.resolvedUrl("icons/menu_icon_gauge_needle.png")
              tint: root.templateColor
              transform: Rotation {
                origin.x: 12
                origin.y: 14
                angle: root.needleRotation
              }
            }
          }
        }
      }
    }
  }
}
