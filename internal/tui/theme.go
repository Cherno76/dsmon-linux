package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type palette struct {
	Title       string
	Accent      string
	Muted       string
	OK          string
	Warn        string
	Error       string
	Down        string
	Up          string
	BoxAccount  string
	BoxActivity string
	BoxTraffic  string
	BoxRequests string
	SelectedFG  string
	SelectedBG  string
	DivLine     string

	TempStart string
	TempMid   string
	TempEnd   string

	DownloadStart string
	DownloadMid   string
	DownloadEnd   string
	UploadStart   string
	UploadMid     string
	UploadEnd     string
}

// fallbackPalette 保留原来的 Tokyo Night 配色，供非 Omarchy 环境或主题文件损坏时使用。
func fallbackPalette() palette {
	return palette{
		Title:         "#a9b1d6",
		Accent:        "#7aa2f7",
		Muted:         "#565f89",
		OK:            "#9ece6a",
		Warn:          "#e0af68",
		Error:         "#f7768e",
		Down:          "#f7768e",
		Up:            "#7aa2f7",
		BoxAccount:    "#9ece6a",
		BoxActivity:   "#ad8ee6",
		BoxTraffic:    "#f7768e",
		BoxRequests:   "#7aa2f7",
		SelectedFG:    "#7aa2f7",
		SelectedBG:    "#292e42",
		DivLine:       "#414868",
		TempStart:     "#9ece6a",
		TempMid:       "#e0af68",
		TempEnd:       "#f7768e",
		DownloadStart: "#e0af68",
		DownloadMid:   "#f7768e",
		DownloadEnd:   "#f7768e",
		UploadStart:   "#9ece6a",
		UploadMid:     "#449dab",
		UploadEnd:     "#7aa2f7",
	}
}

func loadPalette() palette {
	theme, ok := loadPaletteIfAvailable()
	if ok {
		return theme
	}
	return fallbackPalette()
}

func loadPaletteIfAvailable() (palette, bool) {
	path := currentBtopThemePath()
	if path == "" {
		return palette{}, false
	}
	colors, err := parseBtopTheme(path)
	if err != nil {
		return palette{}, false
	}
	return paletteFromBtop(colors), true
}

func currentBtopThemePath() string {
	configRoot := xdgHome("XDG_CONFIG_HOME", ".config")
	candidates := make([]string, 0, 4)
	if configRoot != "" {
		configPath := filepath.Join(configRoot, "btop", "btop.conf")
		themeName := btopColorTheme(configPath)
		if themeName != "" && !strings.EqualFold(themeName, "current") &&
			!strings.EqualFold(themeName, "default") && !strings.EqualFold(themeName, "tty") {
			name := filepath.Base(themeName)
			candidates = append(candidates,
				filepath.Join(configRoot, "btop", "themes", name+".theme"),
				filepath.Join("/usr/share/btop/themes", name+".theme"))
		}
	}
	stateRoot := xdgHome("XDG_STATE_HOME", filepath.Join(".local", "state"))
	if stateRoot != "" {
		candidates = append(candidates,
			filepath.Join(stateRoot, "omarchy", "current", "theme", "btop.theme"))
	}
	if configRoot != "" {
		candidates = append(candidates,
			filepath.Join(configRoot, "btop", "themes", "current.theme"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func xdgHome(envName, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, fallback)
}

func btopColorTheme(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(stripThemeComment(scanner.Text()))
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.ToLower(strings.TrimSpace(key)) != "color_theme" {
			continue
		}
		return parseQuotedThemeValue(strings.TrimSpace(value))
	}
	return ""
}

func stripThemeComment(line string) string {
	inQuotes := false
	for index, value := range line {
		switch value {
		case '"':
			inQuotes = !inQuotes
		case '#':
			if !inQuotes {
				return line[:index]
			}
		}
	}
	return line
}

func parseBtopTheme(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(stripThemeComment(scanner.Text()))
		if !strings.HasPrefix(line, "theme[") {
			continue
		}
		closing := strings.IndexByte(line, ']')
		if closing <= len("theme[") {
			continue
		}
		key := strings.TrimSpace(line[len("theme["):closing])
		rest := strings.TrimSpace(line[closing+1:])
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		value := parseQuotedThemeValue(strings.TrimSpace(strings.TrimPrefix(rest, "=")))
		if color, ok := normalizeHexColor(value); ok {
			values[key] = color
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseQuotedThemeValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '"' {
		if end := strings.IndexByte(raw[1:], '"'); end >= 0 {
			if value, err := strconv.Unquote(raw[:end+2]); err == nil {
				return value
			}
		}
	}
	return raw
}

func normalizeHexColor(raw string) (string, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if len(raw) != 7 || raw[0] != '#' {
		return "", false
	}
	if _, err := strconv.ParseUint(raw[1:], 16, 24); err != nil {
		return "", false
	}
	return raw, true
}

func paletteFromBtop(colors map[string]string) palette {
	result := fallbackPalette()
	use := func(target *string, keys ...string) {
		for _, key := range keys {
			if value := colors[key]; value != "" {
				*target = value
				return
			}
		}
	}
	use(&result.Title, "title", "main_fg")
	use(&result.Accent, "hi_fg", "selected_fg")
	use(&result.Muted, "inactive_fg")
	use(&result.OK, "temp_start")
	use(&result.Warn, "temp_mid")
	use(&result.Error, "temp_end")
	use(&result.Down, "download_end")
	use(&result.Up, "upload_end")
	use(&result.BoxAccount, "mem_box")
	use(&result.BoxActivity, "cpu_box")
	use(&result.BoxTraffic, "net_box")
	use(&result.BoxRequests, "proc_box")
	use(&result.SelectedFG, "selected_fg", "hi_fg")
	use(&result.SelectedBG, "selected_bg")
	use(&result.DivLine, "div_line")
	use(&result.TempStart, "temp_start")
	use(&result.TempMid, "temp_mid")
	use(&result.TempEnd, "temp_end")
	use(&result.DownloadStart, "download_start")
	use(&result.DownloadMid, "download_mid")
	use(&result.DownloadEnd, "download_end")
	use(&result.UploadStart, "upload_start")
	use(&result.UploadMid, "upload_mid")
	use(&result.UploadEnd, "upload_end")
	return result
}
