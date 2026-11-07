package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"dsmon/internal/ipc"
)

// Run 启动全屏 TUI。daemon 不在线时仍进入界面，由状态栏提示断连。
func Run(client *ipc.Client, ascii bool) error {
	program := tea.NewProgram(NewModel(client, ascii), tea.WithAltScreen())
	_, err := program.Run()
	return err
}
