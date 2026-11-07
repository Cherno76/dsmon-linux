// Package pricing 负责计费规则的加载、峰谷判定、模型匹配与费用计算。
//
// 这一层不依赖数据库、终端或全局时钟；调用方必须把请求时刻显式传进来。
package pricing

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Source 记录一份规则从哪里来。字符串值与 Mac 版 Swift 枚举的 rawValue 保持一致，
// 因为它会原样写进 pricing_revision.source。
type Source string

const (
	SourceUserOverride Source = "userOverride"
	SourceBundled      Source = "bundled"
	SourceRemote       Source = "remote"
	SourceNone         Source = "none"
)

// Strategy 是 pricing_strategy.json 的原始结构。
type Strategy struct {
	Comment  string          `json:"comment,omitempty"`
	DeepSeek DeepSeekPricing `json:"deepseek"`
}

type DeepSeekPricing struct {
	PeakHours *PeakHours         `json:"peak_hours,omitempty"`
	Models    map[string]Buckets `json:"models"`
}

type PeakHours struct {
	Weekday [][]int `json:"weekday,omitempty"`
	Weekend [][]int `json:"weekend,omitempty"`
}

// Buckets 是一个模型的工作日/周末、峰时/谷时价格。价格为人民币元 / 1M tokens。
type Buckets struct {
	Offpeak        *Rate `json:"offpeak"`
	Peak           *Rate `json:"peak,omitempty"`
	WeekendOffpeak *Rate `json:"weekend_offpeak,omitempty"`
	WeekendPeak    *Rate `json:"weekend_peak,omitempty"`
}

type Rate struct {
	Miss float64 `json:"miss"`
	Hit  float64 `json:"hit"`
	Out  float64 `json:"out"`
}

// Loaded 是一次加载的完整结果。Text 与 Fingerprint 会随修订一起入库留档。
type Loaded struct {
	Strategy    *Strategy
	Source      Source
	Fingerprint string
	Text        string
	IsFallback  bool
	Warnings    []string
}

//go:embed pricing_strategy.json
var bundledJSON []byte

// DefaultOverridePath 返回 Linux 版允许用户覆盖内置规则的路径。
func DefaultOverridePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "dsmon", "pricing_strategy.json")
}

// LoadDefault 读取用户覆盖文件；不存在或坏掉时回退到内置快照。
func LoadDefault(logf func(string)) Loaded {
	return Load(DefaultOverridePath(), bundledJSON, logf)
}

// Load 从一般路径读取规则。传入 bundled 是为了让测试能构造“内置文件也不可用”的场景。
//
// 覆盖文件存在但解析失败时必须标记 IsFallback：调用方不能把一次手误当成
// “从此刻起换用内置价”，否则历史费用会被静默改写。
func Load(overridePath string, bundled []byte, logf func(string)) Loaded {
	if logf == nil {
		logf = func(string) {}
	}

	if overridePath != "" {
		if data, err := os.ReadFile(overridePath); err == nil {
			text := string(data)
			if strategy, parseErr := Parse(text); parseErr == nil {
				return Loaded{
					Strategy:    strategy,
					Source:      SourceUserOverride,
					Fingerprint: Fingerprint(text),
					Text:        text,
				}
			}
			logf(fmt.Sprintf("覆盖文件解析失败，暂用内置计费表（时间轴不动、历史费用保持不变）: %s", overridePath))
			return fallback(bundled, logf)
		}
	}

	if strategy, err := Parse(string(bundled)); err == nil {
		text := string(bundled)
		return Loaded{
			Strategy:    strategy,
			Source:      SourceBundled,
			Fingerprint: Fingerprint(text),
			Text:        text,
		}
	}
	logf("无法读取内置计费快照（用户覆盖与内置文件均不可用）")
	return emptyLoaded()
}

func fallback(bundled []byte, logf func(string)) Loaded {
	text := string(bundled)
	if strategy, err := Parse(text); err == nil {
		return Loaded{
			Strategy:    strategy,
			Source:      SourceBundled,
			Fingerprint: Fingerprint(text),
			Text:        text,
			IsFallback:  true,
		}
	}
	logf("内置计费快照也不可用")
	return emptyLoaded()
}

func emptyLoaded() Loaded {
	return Loaded{
		Strategy:    &Strategy{DeepSeek: DeepSeekPricing{Models: map[string]Buckets{}}},
		Source:      SourceNone,
		Fingerprint: "none",
		IsFallback:  true,
	}
}

// Parse 解析并做最低限度校验。缺 deepseek.models 的规则视为坏规则，
// 因为那种文件会让所有请求静默变成 ¥0。
func Parse(text string) (*Strategy, error) {
	var strategy Strategy
	if err := json.Unmarshal([]byte(text), &strategy); err != nil {
		return nil, fmt.Errorf("parse pricing strategy: %w", err)
	}
	if strategy.DeepSeek.Models == nil || len(strategy.DeepSeek.Models) == 0 {
		return nil, errors.New("pricing strategy has no models")
	}
	for name, buckets := range strategy.DeepSeek.Models {
		if buckets.Offpeak == nil {
			return nil, fmt.Errorf("pricing strategy model %q has no offpeak rate", name)
		}
	}
	return &strategy, nil
}

// Fingerprint 返回规则原文 SHA-256 的前 16 位，与 Mac 版一致。
func Fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}
