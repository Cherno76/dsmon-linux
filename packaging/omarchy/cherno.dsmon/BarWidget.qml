import QtQuick
import Quickshell
import qs.Commons
import qs.Ui

BarWidget {
  id: root
  moduleName: "cherno.dsmon"

  Translations {
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

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: "\uf080"
    slotSize: Style.bar.statusSlot
    tooltipText: strings.t("tooltip")
    onPressed: root.openDashboard()
  }
}
