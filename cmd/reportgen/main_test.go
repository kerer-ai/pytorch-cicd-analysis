package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pytorch-cicd-analysis/conf"
	"pytorch-cicd-analysis/internal/report"
)

const coreJSONL = `{"total_file":1,"total_cases":2}
{"file_path": "test/test_a.py", "case_count": 2, "cases": [{"nodeid": "test/test_a.py::test_ok", "status": "passed", "duration": 0.1, "file": "test/test_a.py", "case_idx": 0}, {"nodeid": "test/test_a.py::test_bad", "status": "failed", "duration": 0.2, "message": "AssertionError: boom", "file": "test/test_a.py", "case_idx": 1}]}
`

// 与 coreJSONL 存在 nodeid 交叉（test_bad），用于验证冲突语义：首现保位、后到更内容。
const tensorJSONL = `{"total_file":1,"total_cases":2}
{"file_path": "test/test_b.py", "case_count": 2, "cases": [{"nodeid": "test/test_b.py::test_x", "status": "skipped", "duration": 0, "message": "skip: not supported", "file": "test/test_b.py", "case_idx": 0}, {"nodeid": "test/test_a.py::test_bad", "status": "error", "duration": 0.3, "message": "RuntimeError: later", "file": "test/test_a.py", "case_idx": 1}]}
`

const skippedJSON = `{"total_skipped": 2, "sources": ["disabled_testcases.json", "running_skiped_testcases.json"],
"skipped_cases": [
  {"nodeid": "test/test_a.py::test_skip1", "file": "test/test_a.py", "skip_reason": "disabled reason", "skip_category": "cat-a", "skip_source": "disabled_testcases.json", "issue": null},
  {"nodeid": "test/test_a.py::test_skip1", "file": "test/test_a.py", "skip_reason": "dup", "skip_category": "cat-a", "skip_source": "disabled_testcases.json", "issue": null},
  {"nodeid": "test/test_b.py::test_x", "file": "test/test_b.py", "skip_reason": "running skip reason", "skip_source": "running_skiped_testcases.json"}
]}`

func writeArtifactZip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, content := range map[string]string{
		"npu-full-test-summary.json":         `{"totals": {"total": 4}}`,
		"core_cases_results_by_file.jsonl":   coreJSONL,
		"tensor_cases_results_by_file.jsonl": tensorJSONL,
		"skipped_cases.json":                 skippedJSON,
	} {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildInputOrderingAndConflicts(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "npu-full-test-summary.zip")
	writeArtifactZip(t, zipPath)

	extractDir := filepath.Join(dir, "extracted")
	if err := unzipAll(zipPath, extractDir); err != nil {
		t.Fatalf("unzipAll: %v", err)
	}

	in, err := buildInput(123, extractDir, "", "", "")
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}

	if len(in.Cases) != 3 { // test_bad 去重后 3 条
		t.Fatalf("cases = %d, want 3", len(in.Cases))
	}
	// glob 序：core 先于 tensor；test_bad 首现在 core 的位置，内容被 tensor 覆盖
	if in.Cases[1].NodeID != "test/test_a.py::test_bad" {
		t.Fatalf("cases[1] = %s, want test/test_a.py::test_bad", in.Cases[1].NodeID)
	}
	if in.Cases[1].Status != "error" || in.Cases[1].ErrorMessage != "RuntimeError: later" {
		t.Fatalf("conflict content not updated by later file: %+v", in.Cases[1])
	}
	if in.Cases[0].NodeID != "test/test_a.py::test_ok" || in.Cases[2].NodeID != "test/test_b.py::test_x" {
		t.Fatalf("unexpected order: %s, %s", in.Cases[0].NodeID, in.Cases[2].NodeID)
	}

	if len(in.FileResults) != 2 {
		t.Fatalf("fileResults = %d, want 2", len(in.FileResults))
	}
	for _, fr := range in.FileResults {
		if fr.FilePath == "test/test_b.py" && fr.TotalCases != 2 {
			t.Fatalf("test_b total = %d, want 2", fr.TotalCases)
		}
	}

	if len(in.Skipped) != 2 { // nodeid 首现去重
		t.Fatalf("skipped = %d, want 2", len(in.Skipped))
	}
	if in.Skipped[0].SkipReason != "disabled reason" || in.Skipped[1].SkipSource != "running_skiped_testcases.json" {
		t.Fatalf("skipped order/dedup wrong: %+v", in.Skipped)
	}
}

func TestGenerateReportsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "npu-full-test-summary.zip")
	writeArtifactZip(t, zipPath)

	extractDir := filepath.Join(dir, "extracted")
	if err := unzipAll(zipPath, extractDir); err != nil {
		t.Fatalf("unzipAll: %v", err)
	}
	in, err := buildInput(123, extractDir, "", "", "")
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}

	outDir := filepath.Join(dir, "out")
	result, err := report.GenerateReports(*in, outDir, conf.FS)
	if err != nil {
		t.Fatalf("GenerateReports: %v", err)
	}
	if result.FileCount != 6 {
		t.Fatalf("fileCount = %d, want 6", result.FileCount)
	}
	workDir := filepath.Dir(result.ZipPath)
	for _, name := range []string{
		"report.zip", "all_testcases.xlsx", "failed_testcases.xlsx",
		"summary_report.xlsx", "all_testcases_report.md",
		"disabled_testcases.json", "running_skiped_testcases.json",
	} {
		if _, err := os.Stat(filepath.Join(workDir, name)); err != nil {
			t.Errorf("missing output %s: %v", name, err)
		}
	}
	md, err := os.ReadFile(filepath.Join(workDir, "all_testcases_report.md"))
	if err != nil {
		t.Fatal(err)
	}
	// markdown 为统计汇总（不含单个 nodeid）：1 passed + 1 error + 1 skipped
	if !strings.Contains(string(md), "| **合计** | **3** | **1** | **0** | **1** | **1** |") {
		t.Errorf("markdown summary row wrong:\n%s", string(md))
	}
}

func TestBuildInputNoJSONL(t *testing.T) {
	dir := t.TempDir()
	if _, err := buildInput(1, dir, "", "", ""); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

// TestBuildInputTestReportsPath 新式输入：用例明细来自 test-reports-*.zip（字母序），
// FileResults/skipped 来自 artifact zip，无 nodeid 去重，precollect 来自 md。
func TestBuildInputTestReportsPath(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "npu-full-test-summary.zip")
	writeArtifactZip(t, zipPath)

	extractDir := filepath.Join(dir, "extracted")
	if err := unzipAll(zipPath, extractDir); err != nil {
		t.Fatalf("unzipAll: %v", err)
	}

	// 两个 test-reports zip（字母序 b-before-a 文件名验证排序）
	trDir := filepath.Join(dir, "reports")
	if err := os.MkdirAll(trDir, 0755); err != nil {
		t.Fatal(err)
	}
	mkShardZip := func(path, innerName, casesJSON string) {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		w := zip.NewWriter(f)
		fw, err := w.Create(innerName)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(casesJSON))
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mkShardZip(filepath.Join(trDir, "test-reports-b-1.zip"), "shard_b-1_cases.json",
		`{"shard": 1, "shard_type": "b", "cases": [
		  {"nodeid": "test/test_b.py::t1", "status": "passed", "file": "test/test_b.py"},
		  {"nodeid": "test/test_a.py::t_dup", "status": "failed", "message": "m-b", "file": "test/test_a.py"}
		]}`)
	mkShardZip(filepath.Join(trDir, "test-reports-a-1.zip"), "shard_a-1_cases.json",
		`{"shard": 1, "shard_type": "a", "cases": [
		  {"nodeid": "test/test_a.py::t_dup", "status": "error", "message": "m-a", "file": "test/test_a.py"}
		]}`)
	// 非 test-reports 前缀的 zip 应被忽略
	mkShardZip(filepath.Join(trDir, "other.zip"), "shard_x_cases.json",
		`{"cases": [{"nodeid": "test/x.py::t", "status": "passed", "file": "test/x.py"}]}`)

	cpuMD := filepath.Join(dir, "cpu_full_test.md")
	os.WriteFile(cpuMD, []byte("| 测试文件 | 分片 | 规划用例 |\n| --- | --- | --- |\n| test/test_a.py | a-1 | 12 | test |\n"), 0644)

	in, err := buildInput(7, extractDir, trDir, cpuMD, "")
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}

	// 字母序：a-1 先于 b-1；重复 nodeid 不去重（对齐 Python load_cases）
	if len(in.Cases) != 3 {
		t.Fatalf("cases = %d, want 3 (no dedup)", len(in.Cases))
	}
	if in.Cases[0].NodeID != "test/test_a.py::t_dup" || in.Cases[0].Status != "error" {
		t.Errorf("cases[0] = %+v, want a-1 t_dup error", in.Cases[0])
	}
	if in.Cases[1].NodeID != "test/test_b.py::t1" {
		t.Errorf("cases[1] = %+v, want b-1 t1", in.Cases[1])
	}
	if in.Cases[2].NodeID != "test/test_a.py::t_dup" || in.Cases[2].ErrorMessage != "m-b" {
		t.Errorf("cases[2] = %+v, want b-1 t_dup kept (no dedup)", in.Cases[2])
	}

	// FileResults 仍来自 artifact zip（by_file jsonl）
	if len(in.FileResults) != 2 {
		t.Fatalf("fileResults = %d, want 2", len(in.FileResults))
	}
	if len(in.Skipped) != 2 {
		t.Fatalf("skipped = %d, want 2", len(in.Skipped))
	}

	if in.CPUPrecollect["test/test_a.py"] != 12 {
		t.Errorf("cpuPrecollect = %v, want test/test_a.py=12", in.CPUPrecollect)
	}
	if in.NPUPrecollect != nil {
		t.Errorf("npuPrecollect = %v, want nil", in.NPUPrecollect)
	}
}
