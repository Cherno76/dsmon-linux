package version

import (
	_ "embed"
	"strings"
)

// Current 由 VERSION 文件注入；功能和修复修改后运行 make bump-version。
//
//go:embed VERSION
var raw string

var Current = strings.TrimSpace(raw)
