// reportgen 从 nightly 流水线的 npu-full-test-summary 制品生成社区解耦进展报告。
// 组装逻辑复刻 ascend-ci-analyzer scheduler.go:995-1073（fetchRunDetails JSONL 部分），
// 以 SQLite 写入语义为准保证与网页按钮产物 parity：
//   - test_cases  UNIQUE(run_id,nodeid) ON CONFLICT DO UPDATE → 首现保位、后到更内容，
//     ListAllTestCasesByRun ORDER BY id ASC → glob 序（v2 先 v3 后）+ 文件内用例序
//   - test_file_results  UNIQUE(run_id,file_path,test_type) DO UPDATE → 后到覆盖
//   - skipped_cases  ParseSkippedCases 首现去重序 = ORDER BY id ASC
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"pytorch-cicd-analysis/conf"
	"pytorch-cicd-analysis/internal/artifact"
	"pytorch-cicd-analysis/internal/models"
	"pytorch-cicd-analysis/internal/report"
)

func main() {
	var (
		runID   = flag.Int64("run-id", 0, "GitHub Actions run ID (required)")
		zipPath = flag.String("artifact", "", "path to npu-full-test-summary artifact zip (required)")
		outDir  = flag.String("out", "out", "output directory")
	)
	flag.Parse()

	if *runID <= 0 || *zipPath == "" {
		fmt.Fprintln(os.Stderr, "usage: reportgen -run-id <id> -artifact <npu-full-test-summary.zip> [-out dir]")
		os.Exit(2)
	}

	extractDir, err := os.MkdirTemp("", "reportgen-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(extractDir)

	if err := unzipAll(*zipPath, extractDir); err != nil {
		fatal(fmt.Errorf("unzip artifact: %w", err))
	}

	in, err := buildInput(*runID, extractDir)
	if err != nil {
		fatal(err)
	}

	result, err := report.GenerateReports(*in, *outDir, conf.FS)
	if err != nil {
		fatal(fmt.Errorf("generate reports: %w", err))
	}

	fmt.Printf("run_id=%d\ncases=%d\nfiles=%d\nskipped=%d\nzip=%s\nsize=%d\n",
		*runID, len(in.Cases), result.FileCount, len(in.Skipped), result.ZipPath, result.TotalSize)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "reportgen:", err)
	os.Exit(1)
}

// buildInput 从解压目录构建报告输入，顺序语义见文件头注释。
func buildInput(runID int64, extractDir string) (*report.Input, error) {
	// glob 序 = DB 插入序：v2 (*_cases_results_by_file.jsonl) 先、v3 (shard_*_cases.jsonl) 后
	matches, _ := filepath.Glob(filepath.Join(extractDir, "*_cases_results_by_file.jsonl"))
	v3Matches, _ := filepath.Glob(filepath.Join(extractDir, "shard_*_cases.jsonl"))
	matches = append(matches, v3Matches...)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no case JSONL found under %s", extractDir)
	}

	var ordered []*models.TestCase
	index := map[string]int{} // nodeid -> position in ordered

	fileResults := []*models.TestFileResult{}
	frKey := map[string]*models.TestFileResult{} // file_path\x00test_type -> result

	for _, jsonlPath := range matches {
		baseName := filepath.Base(jsonlPath)
		var testType string
		if strings.HasSuffix(baseName, "_cases_results_by_file.jsonl") {
			testType = strings.TrimSuffix(baseName, "_cases_results_by_file.jsonl")
		} else if strings.HasSuffix(baseName, "_cases.jsonl") {
			raw := strings.TrimSuffix(baseName, "_cases.jsonl")
			if idx := strings.Index(raw, "_"); idx >= 0 {
				testType = raw[idx+1:]
			} else {
				testType = raw
			}
		}
		if testType == "" {
			continue
		}

		data, err := os.ReadFile(jsonlPath)
		if err != nil {
			continue
		}
		fileResultsParsed, err := artifact.ParseCasesJSONL(data)
		if err != nil {
			continue
		}

		for _, fr := range fileResultsParsed {
			filePath := fr.ResolvedFilePath()
			for _, c := range fr.Cases {
				caseFile := c.File
				if caseFile == "" {
					caseFile = filePath
				}
				tc := &models.TestCase{
					RunID: runID, NodeID: c.NodeID, FilePath: caseFile,
					ShardName: "", CaseIdx: c.CaseIdx, Command: c.Command,
					Status: c.Status, DurationMS: int64(c.Duration * 1000),
					ReturnCode: c.ReturnCode, ErrorMessage: c.Message,
					ErrorTraceback: c.Message, TestType: testType,
				}
				if i, ok := index[tc.NodeID]; ok {
					ordered[i] = tc // 保位更内容
				} else {
					index[tc.NodeID] = len(ordered)
					ordered = append(ordered, tc)
				}
			}

			passed, failed, errors, skipped := 0, 0, 0, 0
			for _, c := range fr.Cases {
				switch c.Status {
				case "passed":
					passed++
				case "failed":
					failed++
				case "errors":
					errors++
				case "skipped":
					skipped++
				}
			}
			key := filePath + "\x00" + testType
			if existing, ok := frKey[key]; ok {
				existing.TotalCases = fr.CaseCount
				existing.PassedCases = passed
				existing.FailedCases = failed
				existing.ErrorsCases = errors
				existing.SkippedCases = skipped
			} else {
				nfr := &models.TestFileResult{
					RunID: runID, FilePath: filePath, TestType: testType,
					TotalCases: fr.CaseCount, PassedCases: passed,
					FailedCases: failed, ErrorsCases: errors, SkippedCases: skipped,
				}
				frKey[key] = nfr
				fileResults = append(fileResults, nfr)
			}
		}
	}

	var skippedCases []*models.SkippedCase
	if data, err := os.ReadFile(filepath.Join(extractDir, "skipped_cases.json")); err == nil {
		if sc, perr := artifact.ParseSkippedCases(data); perr == nil {
			skippedCases = sc
		}
	}

	return &report.Input{
		RunID:       runID,
		Cases:       ordered,
		FileResults: fileResults,
		Skipped:     skippedCases,
	}, nil
}

// unzipAll 解压 zip 到 dest（含 zip-slip 防护）。
func unzipAll(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	for _, f := range r.File {
		name := filepath.FromSlash(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(dest, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
