package report

import (
	"bufio"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/xuri/excelize/v2"
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

func parseOps(r io.Reader) ([]string, error) {
	var ops []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			ops = append(ops, line)
		}
	}
	return ops, scanner.Err()
}

// LoadAPIATenFS 从 api-ATen.xlsx 读取未支持的 API/ATen 名称清单。
func LoadAPIATenFS(fsys fs.FS, name string) ([]string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseAPIATen(f)
}

// parseAPIATen 读取 api-ATen.xlsx 的 Sheet1，提取「API名称」列（B）与
// 「依托ATen算子」列（C）。B 列原样加入；C 列按分号拆分后加 aten:: 前缀加入
// （如 mode → aten::mode），避免裸算子名整词匹配时误命中日志源码中的同名变量/参数。
func parseAPIATen(r io.Reader) ([]string, error) {
	xlsx, err := excelize.OpenReader(r)
	if err != nil {
		return nil, err
	}
	defer xlsx.Close()

	rows, err := xlsx.GetRows("Sheet1")
	if err != nil {
		return nil, err
	}

	var names []string
	seen := map[string]bool{}
	for i, row := range rows {
		if i == 0 { // 表头
			continue
		}
		var api, aten string
		if len(row) > 1 {
			api = strings.TrimSpace(row[1])
		}
		if len(row) > 2 {
			aten = strings.TrimSpace(row[2])
		}
		if api != "" && !seen[api] {
			seen[api] = true
			names = append(names, api)
		}
		for _, tok := range strings.Split(aten, ";") {
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
	}
	return names, nil
}
