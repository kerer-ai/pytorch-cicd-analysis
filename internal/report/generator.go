package report

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"pytorch-cicd-analysis/internal/models"
)

type TestCaseInfo struct {
	NodeID  string
	Status  string
	Message string
}

type Input struct {
	RunID       int64
	Cases       []*models.TestCase
	FileResults []*models.TestFileResult
	Skipped     []*models.SkippedCase
}

type ReportResult struct {
	ZipPath   string
	FileCount int
	TotalSize int64
}

const (
	opsName              = "ops.txt"
	summaryTemplateName  = "_summary_template_data.json"
	categoriesConfigName = "failed_categories.json"
	unsupportedName      = "UNSUPPORTED.json"
)

// countKV 保序计数（首现顺序 + 计数），供 markdown 稳定排序使用。
type countKV struct {
	Name  string
	Count int
}

// fileGroup 按插入序（=Python os.listdir sorted 的 zip 遍历序）分组的文件用例。
type fileGroup struct {
	path  string
	cases []TestCaseInfo
}

// GenerateReports 生成社区解耦进展报告（对齐 /tmp/20260827 python 流水线），
// 产物：all_testcases.xlsx（含[黑名单跳过]sheet）、failed_testcases.xlsx、
// summary_report.xlsx、all_testcases_report.md、disabled_testcases.json、running_skiped_testcases.json。
func GenerateReports(in Input, outDir string, confFS fs.FS) (*ReportResult, error) {
	timestamp := time.Now().Format("20060102-150405")
	workDir := filepath.Join(outDir, fmt.Sprintf("%d-%s", in.RunID, timestamp))
	os.MkdirAll(workDir, 0755)

	var cat *Categorizer
	var err error
	if confFS != nil {
		cat, err = NewCategorizer(confFS, categoriesConfigName)
	} else {
		cat, err = NewCategorizer(os.DirFS("."), categoriesConfigName)
	}
	if err != nil {
		return nil, fmt.Errorf("load categories: %w", err)
	}

	var unsupportedCfg *UnsupportedConfig
	if confFS != nil {
		unsupportedCfg, _ = LoadUnsupportedConfig(confFS, unsupportedName)
	} else {
		unsupportedCfg, _ = LoadUnsupportedConfig(os.DirFS("."), unsupportedName)
	}

	var ops []string
	var summaryTemplate *SummaryTemplate
	if confFS != nil {
		ops, _ = LoadOpsFS(confFS, opsName)
		summaryTemplate, err = LoadSummaryTemplateFS(confFS, summaryTemplateName)
	} else {
		summaryTemplate, err = LoadSummaryTemplateFS(os.DirFS("."), summaryTemplateName)
	}
	if err != nil {
		return nil, fmt.Errorf("load summary template: %w", err)
	}

	fileMap := BuildFileMap(summaryTemplate)
	orderedFiles := buildOrderedFiles(in.Cases)

	outputFiles := make([]string, 0, 6)

	f1 := filepath.Join(workDir, "all_testcases.xlsx")
	if err := writeAllTestcases(f1, orderedFiles, fileMap, ops, in.FileResults, in.Skipped, unsupportedCfg); err != nil {
		return nil, fmt.Errorf("all_testcases: %w", err)
	}
	outputFiles = append(outputFiles, f1)

	f2 := filepath.Join(workDir, "failed_testcases.xlsx")
	failedCatCounts, err := writeFailedTestcases(f2, orderedFiles, fileMap, ops, cat, unsupportedCfg)
	if err != nil {
		return nil, fmt.Errorf("failed_testcases: %w", err)
	}
	outputFiles = append(outputFiles, f2)

	f4 := filepath.Join(workDir, "summary_report.xlsx")
	if err := writeSummaryReport(f4, summaryTemplate, orderedFiles); err != nil {
		return nil, fmt.Errorf("summary_report: %w", err)
	}
	outputFiles = append(outputFiles, f4)

	f5 := filepath.Join(workDir, "all_testcases_report.md")
	if err := writeMarkdownReport(f5, orderedFiles, fileMap, ops, failedCatCounts, in.Skipped, unsupportedCfg); err != nil {
		return nil, fmt.Errorf("markdown: %w", err)
	}
	outputFiles = append(outputFiles, f5)

	// 黑名单 JSON 产物（对齐 python Step4/5/5.5：DB skipped + 从 all_testcases 增量补充不支持/运行时跳过用例）
	f6 := filepath.Join(workDir, "disabled_testcases.json")
	f7 := filepath.Join(workDir, "running_skiped_testcases.json")
	if err := writeBlacklistJSONs(f6, f7, orderedFiles, ops, in.Skipped, unsupportedCfg); err != nil {
		return nil, fmt.Errorf("blacklist json: %w", err)
	}
	outputFiles = append(outputFiles, f6, f7)

	zipPath := filepath.Join(workDir, "report.zip")
	totalSize, err := zipFiles(zipPath, outputFiles)
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}

	return &ReportResult{
		ZipPath:   zipPath,
		FileCount: len(outputFiles),
		TotalSize: totalSize,
	}, nil
}

// buildOrderedFiles 按入参顺序（插入序）对文件首现分组，保持文件内用例顺序。
func buildOrderedFiles(cases []*models.TestCase) []fileGroup {
	var groups []fileGroup
	index := map[string]int{}
	for _, c := range cases {
		fp := c.FilePath
		if fp == "" {
			continue
		}
		msg := c.ErrorMessage
		if msg == "" {
			msg = c.ErrorTraceback
		}
		i, ok := index[fp]
		if !ok {
			i = len(groups)
			index[fp] = i
			groups = append(groups, fileGroup{path: fp})
		}
		groups[i].cases = append(groups[i].cases, TestCaseInfo{
			NodeID:  c.NodeID,
			Status:  c.Status,
			Message: msg,
		})
	}
	return groups
}

type testcaseRec struct {
	classification, specialization, file, nodeid, status, message, firstLine, unsupported, reason string
}

func buildAllRecords(orderedFiles []fileGroup, fileMap FileMap, ops []string, unsupportedCfg *UnsupportedConfig) []testcaseRec {
	var records []testcaseRec
	for _, fg := range orderedFiles {
		fc := classifyFile(fileMap, fg.path)
		for _, tc := range fg.cases {
			unsupported, reason := classifyCase(tc.Status, tc.Message, ops, unsupportedCfg)
			firstLine := ""
			if tc.Status == "skipped" {
				firstLine = extractFirstLine(tc.Message)
			}
			message := ""
			switch tc.Status {
			case "failed", "error", "skipped":
				message = tc.Message
			}
			records = append(records, testcaseRec{
				fc.Classification, fc.Specialization, fg.path, tc.NodeID, tc.Status, message, firstLine, unsupported, reason,
			})
		}
	}
	return records
}

var allTestcasesHeaders = []string{"Classification", "Specialization", "File", "nodeid", "执行结果", "报错日志", "skip原生日志首行", "不支持", "不支持原因"}
var allTestcasesWidths = []float64{16, 16, 60, 80, 12, 60, 30, 10, 40}

func writeAllTestcases(path string, orderedFiles []fileGroup, fileMap FileMap, ops []string, fileResults []*models.TestFileResult, skipped []*models.SkippedCase, unsupportedCfg *UnsupportedConfig) error {
	f := excelize.NewFile()
	styles := newReportStyles(f)

	records := buildAllRecords(orderedFiles, fileMap, ops, unsupportedCfg)

	// Sheet 1: all_files（文件级统计，含 0 用例文件；同文件跨 test_type 取 SUM）
	writeAllFilesSheet(f, styles, fileResults, fileMap)

	// Sheet 2: all_testcases（全量用例总表，不合并单元格）
	f.NewSheet("all_testcases")
	writeHeader(f, "all_testcases", allTestcasesHeaders, styles.HeaderFont)
	for i, w := range allTestcasesWidths {
		f.SetColWidth("all_testcases", colLetter(i), colLetter(i), w)
	}
	row := 2
	for _, r := range records {
		writeTestcaseRow(f, "all_testcases", row, r, styles)
		row++
	}

	// Sheet 3+: 分类 sheet（首现顺序，Other 恒最后；joint(A,B) 连续同值合并，不合 C）
	sheetData := map[string][]testcaseRec{}
	var sheetOrder []string
	hasOther := false
	for _, r := range records {
		if r.classification == "Other" {
			hasOther = true
		} else if _, ok := sheetData[r.classification]; !ok {
			sheetOrder = append(sheetOrder, r.classification)
		}
		sheetData[r.classification] = append(sheetData[r.classification], r)
	}
	if hasOther {
		sheetOrder = append(sheetOrder, "Other")
	}
	for _, sheet := range sheetOrder {
		records := sheetData[sheet]
		if len(records) == 0 {
			continue
		}
		f.NewSheet(sheet)
		writeHeader(f, sheet, allTestcasesHeaders, styles.HeaderFont)
		for i, w := range allTestcasesWidths {
			f.SetColWidth(sheet, colLetter(i), colLetter(i), w)
		}
		row := 2
		mergeStart := row
		prevCls, prevSpec := records[0].classification, records[0].specialization
		for _, r := range records {
			writeTestcaseRow(f, sheet, row, r, styles)
			if r.classification != prevCls || r.specialization != prevSpec {
				if row-1 > mergeStart {
					f.MergeCell(sheet, cellName(0, mergeStart), cellName(0, row-1))
					f.MergeCell(sheet, cellName(1, mergeStart), cellName(1, row-1))
				}
				mergeStart = row
				prevCls, prevSpec = r.classification, r.specialization
			}
			row++
		}
		if row-1 > mergeStart {
			f.MergeCell(sheet, cellName(0, mergeStart), cellName(0, row-1))
			f.MergeCell(sheet, cellName(1, mergeStart), cellName(1, row-1))
		}
	}

	// 最后一个 sheet：[黑名单跳过]（对齐 python Step4：追加进 all_testcases.xlsx）
	writeBlacklistSheet(f, styles, skipped, fileMap)

	return f.SaveAs(path)
}

// writeBlacklistSheet 写「黑名单跳过」sheet：8 列，A/B 来自 FileMap，
// skip来源按 disabled/running 填色，A/B/C 逐列合并（对齐 python）。
func writeBlacklistSheet(f *excelize.File, styles *ReportStyles, skipped []*models.SkippedCase, fileMap FileMap) {
	headers := []string{"Classification", "Specialization", "File", "nodeid", "skip来源", "skip分类", "skip原因", "issue"}
	widths := []float64{20, 25, 55, 90, 25, 22, 80, 20}

	f.NewSheet("黑名单跳过")
	writeHeader(f, "黑名单跳过", headers, styles.HeaderFont)
	for i, w := range widths {
		f.SetColWidth("黑名单跳过", colLetter(i), colLetter(i), w)
	}

	row := 2
	for _, sc := range skipped {
		fc := classifyFile(fileMap, sc.FilePath)
		category := sc.SkipCategory
		if sc.SkipSource == "running_skiped_testcases.json" && category == "" {
			category = "Running Skiped"
		}
		for col, v := range []string{fc.Classification, fc.Specialization, sc.FilePath, sc.NodeID} {
			cell := cellName(col, row)
			f.SetCellValue("黑名单跳过", cell, v)
			f.SetCellStyle("黑名单跳过", cell, cell, styles.VCenter)
		}
		sourceCell := cellName(4, row)
		f.SetCellValue("黑名单跳过", sourceCell, sc.SkipSource)
		if sc.SkipSource == "disabled_testcases.json" {
			f.SetCellStyle("黑名单跳过", sourceCell, sourceCell, styles.BlackSkipFill)
		} else {
			f.SetCellStyle("黑名单跳过", sourceCell, sourceCell, styles.RunningSkipFill)
		}
		f.SetCellValue("黑名单跳过", cellName(5, row), category)
		f.SetCellStyle("黑名单跳过", cellName(5, row), cellName(5, row), styles.Center)
		f.SetCellValue("黑名单跳过", cellName(6, row), sc.SkipReason)
		f.SetCellStyle("黑名单跳过", cellName(6, row), cellName(6, row), styles.WrapVCenter)
		f.SetCellValue("黑名单跳过", cellName(7, row), sc.Issue)
		f.SetCellStyle("黑名单跳过", cellName(7, row), cellName(7, row), styles.Center)
		row++
	}
	if row > 3 {
		mergeIdenticalCells(f, "黑名单跳过", 2, row-1, []int{0, 1, 2})
	}
}

func writeTestcaseRow(f *excelize.File, sheet string, row int, r testcaseRec, styles *ReportStyles) {
	vals := []string{r.classification, r.specialization, r.file, r.nodeid, r.status, r.message, r.firstLine, r.unsupported, r.reason}
	for col, v := range vals {
		cell := cellName(col, row)
		f.SetCellValue(sheet, cell, v)
		f.SetCellStyle(sheet, cell, cell, allTestcaseBaseStyle(styles, col))
	}
	switch r.status {
	case "passed":
		f.SetCellStyle(sheet, cellName(4, row), cellName(4, row), styles.PassedFill)
	case "failed", "error":
		f.SetCellStyle(sheet, cellName(4, row), cellName(4, row), styles.FailedFill)
	case "skipped":
		f.SetCellStyle(sheet, cellName(4, row), cellName(4, row), styles.SkippedFill)
	}
	if r.unsupported == "是" {
		f.SetCellStyle(sheet, cellName(7, row), cellName(7, row), styles.UnsupportedFill)
	}
}

func writeAllFilesSheet(f *excelize.File, styles *ReportStyles, fileResults []*models.TestFileResult, fileMap FileMap) {
	f.SetSheetName("Sheet1", "all_files")
	headers := []string{"Classification", "Specialization", "File", "num"}
	widths := []float64{16, 16, 60, 10}
	writeHeader(f, "all_files", headers, styles.HeaderFont)
	for i, w := range widths {
		f.SetColWidth("all_files", colLetter(i), colLetter(i), w)
	}

	counts := map[string]int{}
	for _, fr := range fileResults {
		if fr.FilePath == "" {
			continue
		}
		counts[fr.FilePath] += fr.TotalCases
	}

	type fileRec struct {
		classification, specialization, file string
		num                                  int
	}
	recs := make([]fileRec, 0, len(counts))
	for fp, num := range counts {
		fc := classifyFile(fileMap, fp)
		recs = append(recs, fileRec{fc.Classification, fc.Specialization, fp, num})
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].classification != recs[j].classification {
			return recs[i].classification < recs[j].classification
		}
		if recs[i].specialization != recs[j].specialization {
			return recs[i].specialization < recs[j].specialization
		}
		return recs[i].file < recs[j].file
	})

	row := 2
	mergeStart := row
	var prevCls, prevSpec string
	for i, r := range recs {
		f.SetCellValue("all_files", cellName(0, row), r.classification)
		f.SetCellStyle("all_files", cellName(0, row), cellName(0, row), styles.VCenter)
		f.SetCellValue("all_files", cellName(1, row), r.specialization)
		f.SetCellStyle("all_files", cellName(1, row), cellName(1, row), styles.VCenter)
		f.SetCellValue("all_files", cellName(2, row), r.file)
		f.SetCellStyle("all_files", cellName(2, row), cellName(2, row), styles.VCenter)
		f.SetCellValue("all_files", cellName(3, row), r.num)
		f.SetCellStyle("all_files", cellName(3, row), cellName(3, row), styles.Center)
		if i > 0 && (r.classification != prevCls || r.specialization != prevSpec) {
			if row-1 > mergeStart {
				f.MergeCell("all_files", cellName(0, mergeStart), cellName(0, row-1))
				f.MergeCell("all_files", cellName(1, mergeStart), cellName(1, row-1))
			}
			mergeStart = row
		}
		prevCls, prevSpec = r.classification, r.specialization
		row++
	}
	if row-1 > mergeStart {
		f.MergeCell("all_files", cellName(0, mergeStart), cellName(0, row-1))
		f.MergeCell("all_files", cellName(1, mergeStart), cellName(1, row-1))
	}
}

func allTestcaseBaseStyle(styles *ReportStyles, col int) int {
	switch col {
	case 4, 7, 8: // E/H/I 居中
		return styles.Center
	case 5, 6: // F/G 换行
		return styles.WrapVCenter
	default: // A/B/C/D/J 垂直居中
		return styles.VCenter
	}
}

func writeFailedTestcases(path string, orderedFiles []fileGroup, fileMap FileMap, ops []string, cat *Categorizer, unsupportedCfg *UnsupportedConfig) ([]countKV, error) {
	f := excelize.NewFile()
	styles := newReportStyles(f)

	// 15 列，对齐 /tmp/20260827 classify_failed.py FAILED_COLUMNS
	headers := []string{"Classification", "Specialization", "File", "nodeid", "执行结果", "报错日志", "失败大类", "分类依据", "可能是CANN问题", "是否转CANN", "是否屏蔽", "屏蔽原因", "跟踪链接", "是否可修复", "备注"}
	widths := []float64{20, 25, 55, 90, 14, 80, 22, 50, 16, 14, 14, 20, 30, 14, 30}

	type rec struct {
		classification, specialization, file, nodeid, status, message, category, evidence string
	}

	var allRecords []rec
	grouped := map[string][]rec{}
	for _, fg := range orderedFiles {
		fc := classifyFile(fileMap, fg.path)
		for _, tc := range fg.cases {
			if tc.Status != "failed" && tc.Status != "error" {
				continue
			}
			unsupported, _ := classifyCase(tc.Status, tc.Message, ops, unsupportedCfg)
			if unsupported == "是" {
				continue
			}
			category, evidence := cat.Categorize(tc.Message)
			key := fc.Classification + "+" + fc.Specialization
			r := rec{fc.Classification, fc.Specialization, fg.path, tc.NodeID, tc.Status, tc.Message, category, evidence}
			allRecords = append(allRecords, r)
			grouped[key] = append(grouped[key], r)
		}
	}

	categoryCounts := map[string]int{}
	var categoryOrder []string
	for _, r := range allRecords {
		if _, ok := categoryCounts[r.category]; !ok {
			categoryOrder = append(categoryOrder, r.category)
		}
		categoryCounts[r.category]++
	}
	counts := make([]countKV, 0, len(categoryOrder))
	for _, name := range categoryOrder {
		counts = append(counts, countKV{name, categoryCounts[name]})
	}

	groupKeys := make([]string, 0, len(grouped))
	for k := range grouped {
		groupKeys = append(groupKeys, k)
	}
	sort.Strings(groupKeys)

	writeFailedSheet := func(sheet string, records []rec) {
		if idx, _ := f.GetSheetIndex(sheet); idx < 0 {
			f.NewSheet(sheet)
		}
		writeHeader(f, sheet, headers, styles.HeaderFont)
		for i, w := range widths {
			f.SetColWidth(sheet, colLetter(i), colLetter(i), w)
		}
		row := 2
		for _, r := range records {
			for col, v := range []string{r.classification, r.specialization, r.file, r.nodeid, r.status, r.message} {
				cell := cellName(col, row)
				f.SetCellValue(sheet, cell, v)
				f.SetCellStyle(sheet, cell, cell, styles.ThinBorder)
			}
			f.SetCellStyle(sheet, cellName(4, row), cellName(4, row), styles.FailedFill)

			f.SetCellValue(sheet, cellName(6, row), r.category)
			f.SetCellStyle(sheet, cellName(6, row), cellName(6, row), styles.WrapLeft)
			f.SetCellValue(sheet, cellName(7, row), r.evidence)
			f.SetCellStyle(sheet, cellName(7, row), cellName(7, row), styles.WrapLeft)

			// I 列：可能是CANN问题（分类名命中 UNSUPPORTED.json cann_categories）
			if unsupportedCfg.IsCANNCategory(r.category) {
				f.SetCellValue(sheet, cellName(8, row), "是")
			}

			for col := 8; col < 15; col++ {
				f.SetCellStyle(sheet, cellName(col, row), cellName(col, row), styles.Center)
			}
			row++
		}
		if len(records) > 1 {
			mergeIdenticalCells(f, sheet, 2, row-1, []int{0, 1, 2})
		}
	}

	if len(allRecords) == 0 && len(groupKeys) == 0 {
		f.SetSheetName("Sheet1", "all_failed")
		writeFailedSheet("all_failed", nil)
		return counts, f.SaveAs(path)
	}

	f.SetSheetName("Sheet1", "all_failed")
	writeFailedSheet("all_failed", allRecords)
	for _, key := range groupKeys {
		writeFailedSheet(safeSheetName(key), grouped[key])
	}
	return counts, f.SaveAs(path)
}

type disabledEntry struct {
	Category string `json:"category"`
	Reason   string `json:"reason"`
	Issue    string `json:"issue"`
}

type runningEntry struct {
	Reason string `json:"reason"`
}

// writeBlacklistJSONs 生成 disabled_testcases.json / running_skiped_testcases.json，
// 对齐 /tmp/20260827 parse_blacklist.py（Step4 按 skip_source 拆分 + Step5/5.5 从用例增量补充），
// 键序 = 插入序（skipped 顺序 + all_testcases 行序增量）。
func writeBlacklistJSONs(disabledPath, runningPath string, orderedFiles []fileGroup, ops []string, skipped []*models.SkippedCase, unsupportedCfg *UnsupportedConfig) error {
	typeMap := unsupportedCfg.TypeMap()

	disabled := &orderedDisabledJSON{seen: map[string]bool{}, disabledVals: map[string]disabledEntry{}}
	running := &orderedDisabledJSON{seen: map[string]bool{}, runningVals: map[string]runningEntry{}}

	for _, sc := range skipped {
		if sc.NodeID == "" {
			continue
		}
		switch sc.SkipSource {
		case "running_skiped_testcases.json":
			if running.seen[sc.NodeID] {
				continue
			}
			running.seen[sc.NodeID] = true
			running.keys = append(running.keys, sc.NodeID)
			running.runningVals[sc.NodeID] = runningEntry{Reason: sc.SkipReason}
		case "disabled_testcases.json":
			if disabled.seen[sc.NodeID] {
				continue
			}
			disabled.seen[sc.NodeID] = true
			disabled.keys = append(disabled.keys, sc.NodeID)
			disabled.disabledVals[sc.NodeID] = disabledEntry{
				Category: sc.SkipCategory, Reason: sc.SkipReason, Issue: sc.Issue,
			}
		}
	}

	// 增量补充：all_testcases 中不支持用例（pattern 或 op 命中）-> disabled（Step5）
	// 状态 skipped 且 skip 原生日志命中 running_skiped 关键词 -> running（Step5.5）
	for _, fg := range orderedFiles {
		for _, tc := range fg.cases {
			if tc.NodeID == "" {
				continue
			}
			if tc.Status == "failed" || tc.Status == "error" {
				_, reason := classifyCase(tc.Status, tc.Message, ops, unsupportedCfg)
				if reason != "" && !disabled.seen[tc.NodeID] {
					disabled.seen[tc.NodeID] = true
					disabled.keys = append(disabled.keys, tc.NodeID)
					disabled.disabledVals[tc.NodeID] = disabledEntry{
						Category: typeMap[strings.ToLower(reason)],
						Reason:   reason,
					}
				}
			}
			if tc.Status == "skipped" {
				firstLine := extractFirstLine(tc.Message)
				if firstLine != "" && unsupportedCfg.checkRunningSkiped(firstLine) && !running.seen[tc.NodeID] {
					running.seen[tc.NodeID] = true
					running.keys = append(running.keys, tc.NodeID)
					running.runningVals[tc.NodeID] = runningEntry{Reason: firstLine}
				}
			}
		}
	}

	var b bytes.Buffer
	writeOrderedDisabled(&b, disabled)
	if err := os.WriteFile(disabledPath, b.Bytes(), 0644); err != nil {
		return err
	}
	b.Reset()
	writeOrderedRunning(&b, running)
	return os.WriteFile(runningPath, b.Bytes(), 0644)
}

// jsonStr 输出与 Python json.dumps(s, ensure_ascii=False) 一致的字符串字面量
// （不 HTML 转义）。
func jsonStr(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func writeOrderedDisabled(b *bytes.Buffer, o *orderedDisabledJSON) {
	b.WriteString("{\n")
	for i, k := range o.keys {
		comma := ","
		if i == len(o.keys)-1 {
			comma = ""
		}
		e := o.disabledVals[k]
		inner := fmt.Sprintf(`{"category": %s, "reason": %s, "issue": %s}`,
			jsonStr(e.Category), jsonStr(e.Reason), jsonStr(e.Issue))
		b.WriteString(fmt.Sprintf("    %s: %s%s\n", jsonStr(k), inner, comma))
	}
	b.WriteString("}\n")
}

func writeOrderedRunning(b *bytes.Buffer, o *orderedDisabledJSON) {
	b.WriteString("{\n")
	for i, k := range o.keys {
		comma := ","
		if i == len(o.keys)-1 {
			comma = ""
		}
		e := o.runningVals[k]
		inner := fmt.Sprintf(`{"reason": %s}`, jsonStr(e.Reason))
		b.WriteString(fmt.Sprintf("    %s: %s%s\n", jsonStr(k), inner, comma))
	}
	b.WriteString("}\n")
}

type orderedDisabledJSON struct {
	keys         []string
	seen         map[string]bool
	disabledVals map[string]disabledEntry
	runningVals  map[string]runningEntry
}

func writeHeader(f *excelize.File, sheet string, headers []string, styleID int) {
	for i, h := range headers {
		cell := cellName(i, 1)
		f.SetCellValue(sheet, cell, h)
		f.SetCellStyle(sheet, cell, cell, styleID)
	}
}

func mergeIdenticalCells(f *excelize.File, sheet string, startRow, endRow int, cols []int) {
	for _, col := range cols {
		mergeStart := startRow
		for row := startRow + 1; row <= endRow; row++ {
			prev, _ := f.GetCellValue(sheet, cellName(col, row-1))
			curr, _ := f.GetCellValue(sheet, cellName(col, row))
			if prev != curr {
				if row-1 > mergeStart {
					f.MergeCell(sheet, cellName(col, mergeStart), cellName(col, row-1))
				}
				mergeStart = row
			}
		}
		if endRow > mergeStart {
			f.MergeCell(sheet, cellName(col, mergeStart), cellName(col, endRow))
		}
	}
}

// safeSheetName 按 Python group_key[:31] 语义截断（码点，Excel sheet 名上限 31），
// 并替换 excelize 不接受的字符（真实组名不含，防御性）。
func safeSheetName(name string) string {
	if utf8.RuneCountInString(name) > 31 {
		r := []rune(name)
		name = string(r[:31])
	}
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	return name
}

func cellName(col, row int) string {
	return fmt.Sprintf("%s%d", colLetter(col), row)
}

func colLetter(idx int) string {
	return string(rune('A' + idx))
}

func zipFiles(zipPath string, files []string) (int64, error) {
	f, err := os.Create(zipPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	var totalSize int64
	for _, filePath := range files {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return 0, err
		}
		zw, err := w.Create(filepath.Base(filePath))
		if err != nil {
			return 0, err
		}
		n, err := io.Copy(zw, bytes.NewReader(data))
		if err != nil {
			return 0, err
		}
		totalSize += n
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	return totalSize, nil
}
