pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Effects

Item {
  id: root

  property url source
  property color tint: "#ffffff"

  Image {
    id: maskImage
    anchors.fill: parent
    source: root.source
    fillMode: Image.PreserveAspectFit
    sourceSize.width: Math.round(width * Screen.devicePixelRatio)
    sourceSize.height: Math.round(height * Screen.devicePixelRatio)
    visible: false
    layer.enabled: true
  }

  Rectangle {
    anchors.fill: parent
    color: root.tint
    layer.enabled: true
    layer.effect: MultiEffect {
      maskEnabled: true
      maskSource: maskImage
      maskThresholdMin: 0.5
      maskSpreadAtMin: 1.0
    }
  }
}
