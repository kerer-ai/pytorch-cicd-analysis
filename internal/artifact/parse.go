// Package artifact 解析 npu-full-test-summary 制品中的用例数据文件。
// 提取自 ascend-ci-analyzer internal/github/client.go（ParseCasesJSONL / ParseSkippedCases），
// 解析逻辑保持一致，方法形态改为自由函数（原方法未使用接收者）。
package artifact

import (
	"encoding/json"
	"strings"

	"pytorch-cicd-analysis/internal/models"
)

// FileCasesResult 表示 JSONL 文件中的一行（一个测试文件的用例集合）。
// 同时支持 v2（file_path, case_count）与 v3（test_file）两种格式。
type FileCasesResult struct {
	FilePath  string `json:"file_path"`
	TestFile  string `json:"test_file"`
	CaseCount int    `json:"case_count"`
	Cases     []struct {
		NodeID     string  `json:"nodeid"`
		Status     string  `json:"status"`
		Duration   float64 `json:"duration"`
		ReturnCode int     `json:"returncode"`
		Message    string  `json:"message"`
		Command    string  `json:"command"`
		File       string  `json:"file"`
		CaseIdx    int     `json:"case_idx"`
	} `json:"cases"`
}

// ResolvedFilePath 返回文件路径（v2 file_path 优先，兜底 v3 test_file）。
func (f *FileCasesResult) ResolvedFilePath() string {
	if f.FilePath != "" {
		return f.FilePath
	}
	return f.TestFile
}

// skipped_cases.json 结构。
type SkippedCasesFile struct {
	TotalSkipped int              `json:"total_skipped"`
	Sources      []string         `json:"sources"`
	SkippedCases []RawSkippedCase `json:"skipped_cases"`
}

type RawSkippedCase struct {
	NodeID       string          `json:"nodeid"`
	File         string          `json:"file"`
	SkipReason   string          `json:"skip_reason"`
	SkipCategory string          `json:"skip_category"`
	SkipSource   string          `json:"skip_source"`
	Issue        json.RawMessage `json:"issue"`
}

// ParseSkippedCases 解析 skipped_cases.json，按首现顺序去重（nodeid）。
func ParseSkippedCases(data []byte) ([]*models.SkippedCase, error) {
	var scf SkippedCasesFile
	if err := json.Unmarshal(data, &scf); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(scf.SkippedCases))
	result := make([]*models.SkippedCase, 0, len(scf.SkippedCases))
	for _, raw := range scf.SkippedCases {
		if raw.NodeID == "" || seen[raw.NodeID] {
			continue
		}
		seen[raw.NodeID] = true
		issue := ""
		if len(raw.Issue) > 0 && string(raw.Issue) != "null" {
			issue = string(raw.Issue)
		}
		result = append(result, &models.SkippedCase{
			NodeID:       raw.NodeID,
			FilePath:     raw.File,
			SkipReason:   raw.SkipReason,
			SkipCategory: raw.SkipCategory,
			SkipSource:   raw.SkipSource,
			Issue:        issue,
		})
	}
	return result, nil
}

// ParseCasesJSONL 解析 {type}_cases_results_by_file.jsonl，
// 首行为汇总行（跳过），其余每行一个文件。
func ParseCasesJSONL(data []byte) ([]*FileCasesResult, error) {
	var results []*FileCasesResult
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i == 0 {
			continue // first line is summary
		}
		var fcr FileCasesResult
		if err := json.Unmarshal([]byte(line), &fcr); err != nil {
			continue
		}
		results = append(results, &fcr)
	}
	return results, nil
}
