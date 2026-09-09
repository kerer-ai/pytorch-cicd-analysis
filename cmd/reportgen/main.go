// reportgen 从 nightly 流水线制品生成社区解耦进展报告。
// 两种用例明细来源（对齐 /tmp/20260909 python 流水线的输入架构）：
//   - 新式（推荐）：-test-reports 指向包含 test-reports-*.zip 的目录，
//     用例明细取自各 zip 内 shard_*_cases.json（文件名字母序，无 nodeid 去重，
//     对齐 Python load_cases）；-artifact 仍提供文件级计数与 skipped_cases。
//   - 旧式（兼容）：-artifact 单 zip 的 *_cases_results_by_file.jsonl / shard_*_cases.jsonl，
//     组装逻辑复刻 ascend-ci-analyzer scheduler.go:995-1073（fetchRunDetails JSONL 部分），
//     以 SQLite 写入语义为准保证与网页按钮产物 parity：
//       - test_cases  UNIQUE(run_id,nodeid) ON CONFLICT DO UPDATE → 首现保位、后到更内容，
//         ListAllTestCasesByRun ORDER BY id ASC → glob 序（v2 先 v3 后）+ 文件内用例序
//       - test_file_results  UNIQUE(run_id,file_path,test_type) DO UPDATE → 后到覆盖
//       - skipped_cases  ParseSkippedCases 首现去重序 = ORDER BY id ASC
//
// 可选输入：-cpu-md/-npu-md 指向 full_test.md（文件级规划用例数，填充 all_files 的
// CPU预收集/NPU预收集列，对齐 Python conf/cpu_full_test.md、conf/npu_full_test.md）。
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pytorch-cicd-analysis/conf"
	"pytorch-cicd-analysis/internal/artifact"
	"pytorch-cicd-analysis/internal/models"
	"pytorch-cicd-analysis/internal/report"
)

func main() {
	var (
		runID        = flag.Int64("run-id", 0, "GitHub Actions run ID (required)")
		zipPath      = flag.String("artifact", "", "path to npu-full-test-summary artifact zip (required)")
		testReports  = flag.String("test-reports", "", "directory containing test-reports-*.zip (case details)")
		cpuMD        = flag.String("cpu-md", "", "path to cpu full test summary md (CPU precollect source)")
		npuMD        = flag.String("npu-md", "", "path to npu full test summary md (NPU precollect source)")
		outDir       = flag.String("out", "out", "output directory")
	)
	flag.Parse()

	if *runID <= 0 || *zipPath == "" {
		fmt.Fprintln(os.Stderr, "usage: reportgen -run-id <id> -artifact <npu-full-test-summary.zip> [-test-reports dir] [-cpu-md md] [-npu-md md] [-out dir]")
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

	in, err := buildInput(*runID, extractDir, *testReports, *cpuMD, *npuMD)
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

// buildInput 从解压目录构建报告输入。
// 用例明细：testReportsDir 非空时取 test-reports-*.zip（新式，对齐 Python load_cases，
// 文件名字母序、无 nodeid 去重）；否则取 artifact zip 的 JSONL（旧式 DB 语义）。
// FileResults/skipped 始终来自 artifact zip；precollect 来自 md 文件。
func buildInput(runID int64, extractDir, testReportsDir, cpuMDPath, npuMDPath string) (*report.Input, error) {
	// FileResults：artifact zip 的 by_file jsonl（v2）/ shard jsonl（v3），(file,type) 后到覆盖
	fileResults, err := loadFileResults(extractDir)
	if err != nil {
		return nil, err
	}

	var ordered []*models.TestCase
	if testReportsDir != "" {
		cases, err := loadTestReportsCases(testReportsDir)
		if err != nil {
			return nil, err
		}
		ordered = cases
	} else {
		cases, err := loadJSONLCases(runID, extractDir)
		if err != nil {
			return nil, err
		}
		ordered = cases
	}
	if len(ordered) == 0 {
		return nil, fmt.Errorf("no case source: no test-reports zips under %s and no case JSONL under %s",
			testReportsDir, extractDir)
	}

	var skippedCases []*models.SkippedCase
	if data, err := os.ReadFile(filepath.Join(extractDir, "skipped_cases.json")); err == nil {
		if sc, perr := artifact.ParseSkippedCases(data); perr == nil {
			skippedCases = sc
		}
	}

	var cpuPre, npuPre map[string]int
	if data, err := os.ReadFile(cpuMDPath); err == nil {
		cpuPre = artifact.ParseMDPlannedCounts(data)
	} else if cpuMDPath != "" {
		return nil, fmt.Errorf("read cpu md: %w", err)
	}
	if data, err := os.ReadFile(npuMDPath); err == nil {
		npuPre = artifact.ParseMDPlannedCounts(data)
	} else if npuMDPath != "" {
		return nil, fmt.Errorf("read npu md: %w", err)
	}

	return &report.Input{
		RunID:         runID,
		Cases:         ordered,
		FileResults:   fileResults,
		Skipped:       skippedCases,
		CPUPrecollect: cpuPre,
		NPUPrecollect: npuPre,
	}, nil
}

// loadFileResults 解析 artifact zip 解压目录的文件级结果（v2/v3 JSONL，
// UNIQUE(run_id,file_path,test_type) 后到覆盖语义）。
func loadFileResults(extractDir string) ([]*models.TestFileResult, error) {
	matches, _ := filepath.Glob(filepath.Join(extractDir, "*_cases_results_by_file.jsonl"))
	v3Matches, _ := filepath.Glob(filepath.Join(extractDir, "shard_*_cases.jsonl"))
	matches = append(matches, v3Matches...)

	fileResults := []*models.TestFileResult{}
	frKey := map[string]*models.TestFileResult{}

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
					RunID: 0, FilePath: filePath, TestType: testType,
					TotalCases: fr.CaseCount, PassedCases: passed,
					FailedCases: failed, ErrorsCases: errors, SkippedCases: skipped,
				}
				frKey[key] = nfr
				fileResults = append(fileResults, nfr)
			}
		}
	}
	return fileResults, nil
}

// loadJSONLCases 旧式用例明细：nodeid 首现保位、后到更内容（DB 写入语义）。
func loadJSONLCases(runID int64, extractDir string) ([]*models.TestCase, error) {
	matches, _ := filepath.Glob(filepath.Join(extractDir, "*_cases_results_by_file.jsonl"))
	v3Matches, _ := filepath.Glob(filepath.Join(extractDir, "shard_*_cases.jsonl"))
	matches = append(matches, v3Matches...)

	var ordered []*models.TestCase
	index := map[string]int{} // nodeid -> position in ordered

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
		}
	}
	return ordered, nil
}

// loadTestReportsCases 新式用例明细：test-reports-*.zip（文件名字母序）内
// *_cases.json 的 cases 数组序拼接，无 nodeid 去重（对齐 Python load_cases）。
func loadTestReportsCases(dir string) ([]*models.TestCase, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read test-reports dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "test-reports-") && strings.HasSuffix(n, ".zip") {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	var cases []*models.TestCase
	for _, name := range names {
		zpath := filepath.Join(dir, name)
		if err := func() error {
			zr, err := zip.OpenReader(zpath)
			if err != nil {
				return err
			}
			defer zr.Close()
			for _, f := range zr.File {
				if !strings.HasSuffix(f.Name, "_cases.json") {
					continue
				}
				rc, err := f.Open()
				if err != nil {
					return err
				}
				data, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					return err
				}
				parsed, err := artifact.ParseShardCasesJSON(data)
				if err != nil {
					continue
				}
				cases = append(cases, parsed...)
			}
			return nil
		}(); err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
	}
	return cases, nil
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
