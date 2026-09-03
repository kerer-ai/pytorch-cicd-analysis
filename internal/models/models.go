// Package models 提供报告生成所需的测试用例数据模型。
// 字段定义与 ascend-ci-analyzer internal/models 保持一致（仅保留报告链路所需部分）。
package models

// TestCase 单个测试用例结果（对应 test_cases 表）。
type TestCase struct {
	ID             int64  `json:"id,omitempty"`
	RunID          int64  `json:"run_id"`
	JobID          int64  `json:"job_id,omitempty"`
	NodeID         string `json:"nodeid"`
	FilePath       string `json:"file_path,omitempty"`
	TestClass      string `json:"test_class,omitempty"`
	TestMethod     string `json:"test_method,omitempty"`
	ShardName      string `json:"shard_name,omitempty"`
	CaseIdx        int    `json:"case_idx,omitempty"`
	Command        string `json:"command,omitempty"`
	Status         string `json:"status"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	ReturnCode     int    `json:"returncode,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
	ErrorTraceback string `json:"error_traceback,omitempty"`
	TestType       string `json:"test_type,omitempty"`
}

// TestFileResult 测试文件级聚合结果（对应 test_file_results 表）。
type TestFileResult struct {
	ID           int64  `json:"id,omitempty"`
	RunID        int64  `json:"run_id"`
	FilePath     string `json:"file_path"`
	TestType     string `json:"test_type"`
	Shard        string `json:"shard,omitempty"`
	TotalCases   int    `json:"total_cases"`
	PassedCases  int    `json:"passed_cases"`
	FailedCases  int    `json:"failed_cases"`
	ErrorsCases  int    `json:"errors_cases"`
	SkippedCases int    `json:"skipped_cases"`
	TimeoutCases int    `json:"timeout_cases"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// SkippedCase 黑名单跳过用例（对应 skipped_cases 表）。
type SkippedCase struct {
	ID           int64  `json:"id,omitempty"`
	RunID        int64  `json:"run_id"`
	NodeID       string `json:"nodeid"`
	FilePath     string `json:"file_path,omitempty"`
	SkipReason   string `json:"skip_reason,omitempty"`
	SkipCategory string `json:"skip_category,omitempty"`
	SkipSource   string `json:"skip_source,omitempty"`
	Issue        string `json:"issue,omitempty"`
}
