package report

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"pytorch-cicd-analysis/internal/models"
)

// TestParityWithPythonReference 与 /tmp/20260827 Python 流水线的确定性参考产物
// out_parity 全量内容比对：MD 逐字节、黑名单 JSON 解析态键序键值、
// xlsx sheet 名/单元格值矩阵/合并区域。参考目录可用 PARITY_REF_DIR 覆盖。
// 全量生成 + 双边读回耗时 20-30s，-short 模式跳过。
func TestParityWithPythonReference(t *testing.T) {
	if testing.Short() {
		t.Skip("parity test skipped in short mode")
	}
	refDir := os.Getenv("PARITY_REF_DIR")
	if refDir == "" {
		refDir = "/tmp/20260827"
	}
	outDir := filepath.Join(refDir, "out_parity")
	if _, err := os.Stat(outDir); err != nil {
		t.Skipf("reference output not found at %s (set PARITY_REF_DIR to override): %v", outDir, err)
	}
	confDir := filepath.Join("..", "..", "conf")
	if _, err := os.Stat(filepath.Join(confDir, "_summary_template_data.json")); os.IsNotExist(err) {
		t.Skip("conf/ template files not found")
	}

	in := loadParityInput(t, filepath.Join(refDir, "data"))
	workDir := t.TempDir()
	result, err := GenerateReports(in, workDir, os.DirFS(confDir))
	if err != nil {
		t.Fatalf("GenerateReports: %v", err)
	}
	if result.FileCount != 6 {
		t.Errorf("expected 6 files, got %d", result.FileCount)
	}
	workDir = filepath.Dir(result.ZipPath)

	t.Run("markdown", func(t *testing.T) {
		got, err := os.ReadFile(filepath.Join(workDir, "all_testcases_report.md"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(outDir, "all_testcases_report.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			diffFirst(t, "markdown", got, want)
			t.Error("all_testcases_report.md differs from python reference")
		}
	})

	t.Run("disabled_json", func(t *testing.T) {
		compareOrderedJSONFiles(t,
			filepath.Join(workDir, "disabled_testcases.json"),
			filepath.Join(outDir, "disabled_testcases.json"))
	})

	t.Run("running_json", func(t *testing.T) {
		compareOrderedJSONFiles(t,
			filepath.Join(workDir, "running_skiped_testcases.json"),
			filepath.Join(outDir, "running_skiped_testcases.json"))
	})

	for _, xlsx := range []string{"all_testcases.xlsx", "failed_testcases.xlsx", "summary_report.xlsx"} {
		xlsx := xlsx
		t.Run("xlsx_"+xlsx, func(t *testing.T) {
			compareWorkbooks(t, filepath.Join(workDir, xlsx), filepath.Join(outDir, xlsx))
		})
	}
}

// loadParityInput 从参考 data 目录解析输入（对齐 Python 数据源与顺序）：
// cases 来自 test-reports-*.zip（文件名字母序）的 *_cases.json 数组序；
// fileResults 来自 npu zip 的 *_by_file.jsonl（file_path + case_count）；
// skipped 来自 npu zip 的 skipped_cases.json 数组序。
func loadParityInput(t *testing.T, dataDir string) Input {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var in Input
	for _, name := range names {
		path := filepath.Join(dataDir, name)
		switch {
		case strings.HasPrefix(name, "test-reports-") && strings.HasSuffix(name, ".zip"):
			in.Cases = append(in.Cases, readCasesZip(t, path)...)
		case strings.HasPrefix(name, "npu-full-test-summary") && strings.HasSuffix(name, ".zip"):
			in.FileResults = append(in.FileResults, readFileResultsZip(t, path)...)
			in.Skipped = append(in.Skipped, readSkippedZip(t, path)...)
		}
	}
	if len(in.Cases) == 0 || len(in.FileResults) == 0 || len(in.Skipped) == 0 {
		t.Fatalf("incomplete parity input: cases=%d fileResults=%d skipped=%d",
			len(in.Cases), len(in.FileResults), len(in.Skipped))
	}
	return in
}

func openZip(t *testing.T, path string) *zip.ReadCloser {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	return zr
}

func readCasesZip(t *testing.T, path string) []*models.TestCase {
	t.Helper()
	zr := openZip(t, path)
	defer zr.Close()
	var cases []*models.TestCase
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, "_cases.json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s in %s: %v", f.Name, path, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		var payload struct {
			Cases []struct {
				NodeID  string `json:"nodeid"`
				File    string `json:"file"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("parse %s: %v", f.Name, err)
		}
		for _, c := range payload.Cases {
			cases = append(cases, &models.TestCase{
				NodeID: c.NodeID, FilePath: c.File, Status: c.Status,
				ErrorMessage: c.Message, ErrorTraceback: c.Message,
			})
		}
	}
	return cases
}

func readFileResultsZip(t *testing.T, path string) []*models.TestFileResult {
	t.Helper()
	zr := openZip(t, path)
	defer zr.Close()
	var results []*models.TestFileResult
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, "_by_file.jsonl") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var rec struct {
				FilePath  string `json:"file_path"`
				CaseCount int    `json:"case_count"`
			}
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("parse jsonl line: %v", err)
			}
			if rec.FilePath == "" {
				continue // 汇总行
			}
			results = append(results, &models.TestFileResult{
				FilePath: rec.FilePath, TotalCases: rec.CaseCount,
			})
		}
	}
	return results
}

func readSkippedZip(t *testing.T, path string) []*models.SkippedCase {
	t.Helper()
	zr := openZip(t, path)
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "skipped_cases.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open skipped_cases.json: %v", err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		var payload struct {
			SkippedCases []struct {
				NodeID       string      `json:"nodeid"`
				File         string      `json:"file"`
				SkipReason   string      `json:"skip_reason"`
				SkipCategory string      `json:"skip_category"`
				SkipSource   string      `json:"skip_source"`
				Issue        interface{} `json:"issue"`
			} `json:"skipped_cases"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatalf("parse skipped_cases.json: %v", err)
		}
		var skipped []*models.SkippedCase
		for _, sc := range payload.SkippedCases {
			issue := ""
			if s, ok := sc.Issue.(string); ok {
				issue = s
			}
			skipped = append(skipped, &models.SkippedCase{
				NodeID: sc.NodeID, FilePath: sc.File, SkipReason: sc.SkipReason,
				SkipCategory: sc.SkipCategory, SkipSource: sc.SkipSource, Issue: issue,
			})
		}
		return skipped
	}
	t.Fatalf("skipped_cases.json not found in %s", path)
	return nil
}

// compareOrderedJSONFiles 解析态按序比较（JSON token 流），对齐设计 §5：
// 键序 + 键值全等，非字节比较（容忍转义/空白差异）。
func compareOrderedJSONFiles(t *testing.T, gotPath, wantPath string) {
	t.Helper()
	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	gotTokens := jsonTokens(t, got)
	wantTokens := jsonTokens(t, want)
	if len(gotTokens) != len(wantTokens) {
		t.Errorf("json token count: got %d, want %d", len(gotTokens), len(wantTokens))
		return
	}
	for i := range gotTokens {
		if gotTokens[i] != wantTokens[i] {
			t.Errorf("json token #%d (of %d): got %q, want %q", i, len(gotTokens), gotTokens[i], wantTokens[i])
			return
		}
	}
}

func jsonTokens(t *testing.T, data []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var tokens []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("json token: %v", err)
		}
		tokens = append(tokens, fmt.Sprintf("%v", tok))
	}
	return tokens
}

// compareWorkbooks 对比两个 xlsx：sheet 名列表、逐 sheet 单元格值矩阵、合并区域集合。
func compareWorkbooks(t *testing.T, gotPath, wantPath string) {
	t.Helper()
	got, err := excelize.OpenFile(gotPath)
	if err != nil {
		t.Fatalf("open %s: %v", gotPath, err)
	}
	defer got.Close()
	want, err := excelize.OpenFile(wantPath)
	if err != nil {
		t.Fatalf("open %s: %v", wantPath, err)
	}
	defer want.Close()

	gotSheets := got.GetSheetList()
	wantSheets := want.GetSheetList()
	if len(gotSheets) != len(wantSheets) {
		t.Fatalf("sheet count: got %v, want %v", gotSheets, wantSheets)
	}
	for i := range gotSheets {
		if gotSheets[i] != wantSheets[i] {
			t.Fatalf("sheet #%d: got %q, want %q (all got=%v want=%v)",
				i, gotSheets[i], wantSheets[i], gotSheets, wantSheets)
		}
	}

	for _, sheet := range gotSheets {
		t.Run("sheet_"+sheet, func(t *testing.T) {
			gotRows, _ := got.GetRows(sheet)
			wantRows, _ := want.GetRows(sheet)
			if len(gotRows) != len(wantRows) {
				t.Fatalf("sheet %q row count: got %d, want %d", sheet, len(gotRows), len(wantRows))
			}
			diffs := 0
			for r := 0; r < len(gotRows) && diffs < 5; r++ {
				maxCol := len(gotRows[r])
				if len(wantRows[r]) > maxCol {
					maxCol = len(wantRows[r])
				}
				for c := 0; c < maxCol; c++ {
					gv, wv := cellAt(gotRows[r], c), cellAt(wantRows[r], c)
					if gv != wv {
						t.Errorf("sheet %q cell %s%d: got %q, want %q", sheet,
							string(rune('A'+c)), r+1, truncateCell(gv), truncateCell(wv))
						diffs++
					}
				}
			}
			compareMerges(t, got, want, sheet)
		})
	}
}

func cellAt(row []string, idx int) string {
	if idx < len(row) {
		return row[idx]
	}
	return ""
}

func truncateCell(s string) string {
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60]) + "..."
	}
	return s
}

func compareMerges(t *testing.T, got, want *excelize.File, sheet string) {
	t.Helper()
	norm := func(f *excelize.File) map[string]bool {
		out := map[string]bool{}
		merges, _ := f.GetMergeCells(sheet)
		for _, m := range merges {
			ref := m.GetStartAxis() + "-" + m.GetEndAxis()
			if m.GetStartAxis() == m.GetEndAxis() {
				ref = m.GetStartAxis()
			}
			out[ref] = true
		}
		return out
	}
	gotM, wantM := norm(got), norm(want)
	if len(gotM) != len(wantM) {
		t.Errorf("sheet %q merge count: got %d, want %d", sheet, len(gotM), len(wantM))
	}
	shown := 0
	for ref := range gotM {
		if !wantM[ref] {
			if shown < 5 {
				t.Errorf("sheet %q merge %q not in reference", sheet, ref)
			}
			shown++
		}
	}
	shown = 0
	for ref := range wantM {
		if !gotM[ref] {
			if shown < 5 {
				t.Errorf("sheet %q missing reference merge %q", sheet, ref)
			}
			shown++
		}
	}
}

func diffFirst(t *testing.T, what string, got, want []byte) {
	t.Helper()
	gotLines := strings.Split(string(got), "\n")
	wantLines := strings.Split(string(want), "\n")
	for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
		if gotLines[i] != wantLines[i] {
			t.Errorf("%s first diff at line %d:\n  got:  %s\n  want: %s", what, i+1,
				truncateCell(gotLines[i]), truncateCell(wantLines[i]))
			return
		}
	}
	if len(gotLines) != len(wantLines) {
		t.Errorf("%s line count: got %d, want %d", what, len(gotLines), len(wantLines))
	}
}
