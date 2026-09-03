package report

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

// unsupported.go 对齐 /tmp/20260827 conf/UNSUPPORTED.json：
// cann_categories（CANN 问题分类名单）、unsupported_patterns（不支持模式，AND 逻辑，
// 大小写敏感——对齐 Python `kw in message`）、running_skiped（运行时跳过模式，
// AND 逻辑，不区分大小写——对齐 Python _match_running_skiped）。

const unsupportedConfigName = "UNSUPPORTED.json"

type keywordPattern struct {
	Category string   `json:"category"`
	Keywords []string `json:"keywords"`
}

type unsupportedFile struct {
	CannCategories      []string         `json:"cann_categories"`
	UnsupportedPatterns []keywordPattern `json:"unsupported_patterns"`
	RunningSkiped       []keywordPattern `json:"running_skiped"`
}

type UnsupportedConfig struct {
	cannCategories map[string]bool
	patterns       []keywordPattern
	runningSkiped  []keywordPattern
}

func LoadUnsupportedConfig(fsys fs.FS, name string) (*UnsupportedConfig, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	var uf unsupportedFile
	if err := json.Unmarshal(data, &uf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	cfg := &UnsupportedConfig{
		cannCategories: make(map[string]bool, len(uf.CannCategories)),
		patterns:       uf.UnsupportedPatterns,
		runningSkiped:  uf.RunningSkiped,
	}
	for _, c := range uf.CannCategories {
		cfg.cannCategories[c] = true
	}
	return cfg, nil
}

// IsCANNCategory 分类名命中 cann_categories 名单。
func (u *UnsupportedConfig) IsCANNCategory(category string) bool {
	if u == nil {
		return false
	}
	return u.cannCategories[category]
}

// TypeMap unsupported_patterns 的 reason -> category 映射（黑名单 JSON 的 category 字段）。
func (u *UnsupportedConfig) TypeMap() map[string]string {
	tm := map[string]string{}
	if u == nil {
		return tm
	}
	for _, p := range u.patterns {
		if p.Category != "" {
			tm[strings.ToLower(strings.Join(p.Keywords, " + "))] = p.Category
		}
	}
	return tm
}

// checkUnsupportedPattern 报错日志命中不支持模式（大小写敏感 AND 逻辑，对齐
// Python test_data_to_sheets/classify_failed 的 `kw in message`），返回 "kw1 + kw2"。
func (u *UnsupportedConfig) checkUnsupportedPattern(message string) string {
	if u == nil || message == "" {
		return ""
	}
	for _, p := range u.patterns {
		if len(p.Keywords) == 0 {
			continue
		}
		all := true
		for _, kw := range p.Keywords {
			if !strings.Contains(message, kw) {
				all = false
				break
			}
		}
		if all {
			return strings.Join(p.Keywords, " + ")
		}
	}
	return ""
}

// checkRunningSkiped skip 日志命中运行时跳过关键词（不区分大小写，AND 逻辑）。
func (u *UnsupportedConfig) checkRunningSkiped(skipLog string) bool {
	if u == nil || skipLog == "" {
		return false
	}
	low := strings.ToLower(skipLog)
	for _, p := range u.runningSkiped {
		if len(p.Keywords) == 0 {
			continue
		}
		all := true
		for _, kw := range p.Keywords {
			if !strings.Contains(low, strings.ToLower(kw)) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
