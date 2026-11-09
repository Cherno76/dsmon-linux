package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const ashfallBtopTheme = `
theme[main_bg]="#101513"
theme[main_fg]="#eadcc2"
theme[title]="#eadcc2"
theme[hi_fg]="#d6a65c"
theme[selected_bg]="#26443f"
theme[selected_fg]="#d6a65c"
theme[inactive_fg]="#70796f"
theme[graph_text]="#c9bca6"
theme[cpu_box]="#b87969"
theme[mem_box]="#6f9c82"
theme[net_box]="#c65a3d"
theme[proc_box]="#d6a65c"
theme[div_line]="#70796f"
theme[temp_start]="#6f9c82"
theme[temp_mid]="#d5aa5e"
theme[temp_end]="#c65a3d"
theme[download_start]="#d5aa5e"
theme[download_mid]="#c65a3d"
theme[download_end]="#c65a3d"
theme[upload_start]="#6f9c82"
theme[upload_mid]="#78aaa3"
theme[upload_end]="#668c94"
`

func writeTestTheme(t *testing.T, root, themeName string) string {
	t.Helper()
	configRoot := filepath.Join(root, "config")
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(configRoot, "btop"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "color_theme = \"" + themeName + "\"\n"
	if err := os.WriteFile(filepath.Join(configRoot, "btop", "btop.conf"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	themePath := filepath.Join(stateRoot, "omarchy", "current", "theme", "btop.theme")
	if err := os.MkdirAll(filepath.Dir(themePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(themePath, []byte(ashfallBtopTheme), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	return themePath
}

func TestLoadPaletteReadsOmarchyCurrentBtopTheme(t *testing.T) {
	root := t.TempDir()
	writeTestTheme(t, root, "current")

	theme := loadPalette()
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"title", theme.Title, "#eadcc2"},
		{"accent", theme.Accent, "#d6a65c"},
		{"muted", theme.Muted, "#70796f"},
		{"ok", theme.OK, "#6f9c82"},
		{"warn", theme.Warn, "#d5aa5e"},
		{"error", theme.Error, "#c65a3d"},
		{"account box", theme.BoxAccount, "#6f9c82"},
		{"activity box", theme.BoxActivity, "#b87969"},
		{"traffic box", theme.BoxTraffic, "#c65a3d"},
		{"request box", theme.BoxRequests, "#d6a65c"},
		{"divider", theme.DivLine, "#70796f"},
		{"selected fg", theme.SelectedFG, "#d6a65c"},
		{"selected bg", theme.SelectedBG, "#26443f"},
		{"download start", theme.DownloadStart, "#d5aa5e"},
		{"download end", theme.DownloadEnd, "#c65a3d"},
		{"upload start", theme.UploadStart, "#6f9c82"},
		{"upload end", theme.UploadEnd, "#668c94"},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("%s = %q, want %q", test.name, test.got, test.want)
		}
	}
	if got := theme.trafficGradientColor(0, false); got != "#d5aa5e" {
		t.Errorf("download gradient start = %q", got)
	}
	if got := theme.trafficGradientColor(0, true); got != "#6f9c82" {
		t.Errorf("upload gradient start = %q", got)
	}
	if got := theme.usageBarColor(0); got != "#6f9c82" {
		t.Errorf("usage low color = %q", got)
	}
	if got := theme.usageBarColor(1); got != "#c65a3d" {
		t.Errorf("usage high color = %q", got)
	}
}

func TestLoadPaletteUsesConfiguredBtopTheme(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	if err := os.MkdirAll(filepath.Join(configRoot, "btop", "themes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "btop", "btop.conf"),
		[]byte("color_theme = \"my-theme\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	custom := "theme[hi_fg]=\"#123456\"\n"
	if err := os.WriteFile(filepath.Join(configRoot, "btop", "themes", "my-theme.theme"),
		[]byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "missing-state"))

	if got := loadPalette().Accent; got != "#123456" {
		t.Fatalf("custom btop accent = %q, want #123456", got)
	}
}

func TestLoadPaletteFallsBackWhenNoThemeExists(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	if got, want := loadPalette(), fallbackPalette(); got != want {
		t.Fatalf("fallback palette = %+v, want %+v", got, want)
	}
}

func TestModelRefreshesPaletteOnTick(t *testing.T) {
	root := t.TempDir()
	themePath := writeTestTheme(t, root, "current")
	model := NewModel(nil, false)
	if model.theme.Accent != "#d6a65c" {
		t.Fatalf("initial accent = %q", model.theme.Accent)
	}
	changed := strings.Replace(ashfallBtopTheme, "theme[hi_fg]=\"#d6a65c\"",
		"theme[hi_fg]=\"#abcdef\"", 1)
	if err := os.WriteFile(themePath, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = model.Update(tickMsg(time.Now()))
	if model.theme.Accent != "#abcdef" {
		t.Fatalf("accent after tick = %q, want #abcdef", model.theme.Accent)
	}
}

func TestRenderToneUsesPalette(t *testing.T) {
	theme := fallbackPalette()
	theme.BoxTraffic = "#123456"
	got := renderTone("traffic", toneBoxTraffic, true, theme)
	want := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#123456")).
		Render("traffic")
	if got != want {
		t.Fatalf("renderTone = %q, want %q", got, want)
	}
}
