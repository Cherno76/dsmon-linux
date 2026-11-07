// Package config 集中处理 XDG 路径。配置内容尚未落地，先把路径统一到一处。
package config

import (
	"os"
	"path/filepath"
)

func dataHome() string {
	if value := os.Getenv("XDG_DATA_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".local", "share")
}

func configHome() string {
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".config")
}

func stateHome() string {
	if value := os.Getenv("XDG_STATE_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".local", "state")
}

func DataDir() string { return filepath.Join(dataHome(), "dsmon") }

func DatabasePath() string { return filepath.Join(DataDir(), "usage.db") }

func EncryptionKeyPath() string { return filepath.Join(DataDir(), "enc_key") }

func ConfigPath() string { return filepath.Join(configHome(), "dsmon", "config.toml") }

func PricingOverridePath() string {
	return filepath.Join(configHome(), "dsmon", "pricing_strategy.json")
}

func StateDir() string { return filepath.Join(stateHome(), "dsmon") }

func RuntimeSocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "dsmon.sock")
}
