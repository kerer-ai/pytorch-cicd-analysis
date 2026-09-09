// Package artifact 解析 npu-full-test-summary 制品中的用例数据文件。
// 提取自 ascend-ci-analyzer internal/github/client.go（ParseCasesJSONL / ParseSkippedCases），
// 解析逻辑保持一致，方法形态改为自由函数（原方法未使用接收者）。
package artifact

import (
	"encoding/json"
	"strconv"
	"strings"

	"pytorch-cicd-analysis/internal/models"
)

// Case 单个用例（v2 by_file jsonl / v3 shard jsonl / test-reports shard_*_cases.json 共用）。
type Case struct {
	NodeID     string  `json:"nodeid"`
	Status     string  `json:"status"`
	Duration   float64 `json:"duration"`
	ReturnCode int     `json:"returncode"`
	Message    string  `json:"message"`
	Command    string  `json:"command"`
	File       string  `json:"file"`
	CaseIdx    int     `json:"case_idx"`
}

// FileCasesResult 表示 JSONL 文件中的一行（一个测试文件的用例集合）。
// 同时支持 v2（file_path, case_count）与 v3（test_file）两种格式。
type FileCasesResult struct {
	FilePath  string `json:"file_path"`
	TestFile  string `json:"test_file"`
	CaseCount int    `json:"case_count"`
	Cases     []Case `json:"cases"`
}

// ResolvedFilePath 返回文件路径（v2 file_path 优先，兜底 v3 test_file）。
func (f *FileCasesResult) ResolvedFilePath() string {
	if f.FilePath != "" {
		return f.FilePath
	}
	return f.TestFile
}

// ShardCasesFile 表示 test-reports-*.zip 内 shard_*_cases.json 的结构。
type ShardCasesFile struct {
	Shard     int    `json:"shard"`
	ShardType string `json:"shard_type"`
	Cases     []Case `json:"cases"`
}

// ParseShardCasesJSON 解析 shard_*_cases.json 的用例数组（对齐 Python load_cases
// 读取 data/test-reports-*.zip 内 *_cases.json 的 cases 数组）。
func ParseShardCasesJSON(data []byte) ([]*models.TestCase, error) {
	var scf ShardCasesFile
	if err := json.Unmarshal(data, &scf); err != nil {
		return nil, err
	}
	cases := make([]*models.TestCase, 0, len(scf.Cases))
	for _, c := range scf.Cases {
		cases = append(cases, &models.TestCase{
			NodeID: c.NodeID, FilePath: c.File, Status: c.Status,
			ErrorMessage: c.Message, ErrorTraceback: c.Message,
		})
	}
	return cases, nil
}

// ParseMDPlannedCounts 从 full_test.md 的「测试文件结果汇总」表解析每个文件的
// 规划用例数（对齐 Python _parse_md_planned_counts：行 strip 后去首尾 | 再切分，
// cells[0] 须以 test/ 开头，cells[2] 去千分位后取整数，重复文件累加，`\_` 反转义）。
func ParseMDPlannedCounts(data []byte) map[string]int {
	counts := map[string]int{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "|")
		if line == "" {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		first := strings.TrimSpace(cells[0])
		if !strings.HasPrefix(first, "test/") {
			continue
		}
		filePath := strings.ReplaceAll(first, "\\_", "_")
		numStr := strings.ReplaceAll(strings.TrimSpace(cells[2]), ",", "")
		n, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		counts[filePath] += n
	}
	return counts
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
