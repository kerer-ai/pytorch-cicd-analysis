package report

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"strings"
)

func LoadOps(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseOps(f)
}

func LoadOpsFS(fsys fs.FS, name string) ([]string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseOps(f)
}

// parseOps 读取 disabled_ops.json（字符串数组，保持原顺序）。
func parseOps(r io.Reader) ([]string, error) {
	var ops []string
	if err := json.NewDecoder(r).Decode(&ops); err != nil {
		return nil, err
	}
	return ops, nil
}

// LoadAPIATenFS 从 disabled_api_aten.json 读取未支持的 API/ATen 名称清单。
func LoadAPIATenFS(fsys fs.FS, name string) ([]string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseAPIATen(f)
}

// apiATenConfig 对应 conf/disabled_api_aten.json：两个扁平数组。
type apiATenConfig struct {
	API  []string `json:"api"`
	ATen []string `json:"aten"`
}

// parseAPIATen 读取 disabled_api_aten.json 的 api 与 aten 两个数组。
// api 原样加入；aten 加 aten:: 前缀加入（如 mode → aten::mode），
// 避免裸算子名整词匹配时误命中日志源码中的同名变量/参数。
func parseAPIATen(r io.Reader) ([]string, error) {
	var cfg apiATenConfig
	if err := json.NewDecoder(r).Decode(&cfg); err != nil {
		return nil, err
	}

	var names []string
	seen := map[string]bool{}
	for _, api := range cfg.API {
		api = strings.TrimSpace(api)
		if api != "" && !seen[api] {
			seen[api] = true
			names = append(names, api)
		}
	}
	for _, tok := range cfg.ATen {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		key := "aten::" + tok
		if !seen[key] {
			seen[key] = true
			names = append(names, key)
		}
	}
	return names, nil
}
