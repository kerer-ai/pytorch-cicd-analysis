package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"pytorch-cicd-analysis/internal/models"
)

// testSummaryTemplate 带最小 A/B/C 结构的模板：
// Core sheet: A2=Core, B2=NN, C3/C4 两个文件（B 级联）；B 含 ASCII 计数被剥离。
func testSummaryTemplate() string {
	return `{"sheets": {
		"Core": {"cells": {
			"A1": {"value": "Classification"}, "B1": {"value": "Specialization"}, "C1": {"value": "File"},
			"A2": {"value": "Core"}, "B2": {"value": "NN(22)"},
			"C3": {"value": "test/nn/test_dropout.py"},
			"C4": {"value": "test/nn/test_batchnorm.py"},
			"B5": {"value": "Autograd (11)"},
			"C5": {"value": "test/autograd/test_taylor.py"}
		}, "merged_cells": [], "column_widths": {}}
	}}`
}

func writeTestConf(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "ops.txt"), []byte("aclnnAdd\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "_summary_template_data.json"), []byte(testSummaryTemplate()), 0644); err != nil {
		t.Fatal(err)
	}
	cats, err := os.ReadFile(filepath.Join("..", "..", "conf", "failed_categories.json"))
	if err == nil {
		os.WriteFile(filepath.Join(dir, "failed_categories.json"), cats, 0644)
	}
	unsup, err := os.ReadFile(filepath.Join("..", "..", "conf", "UNSUPPORTED.json"))
	if err == nil {
		os.WriteFile(filepath.Join(dir, "UNSUPPORTED.json"), unsup, 0644)
	}
}

func testInput() Input {
	return Input{
		RunID: 123,
		Cases: []*models.TestCase{
			{NodeID: "test_dropout.py::TestDropout::test_dropout_1d", FilePath: "test/nn/test_dropout.py", Status: "failed", ErrorMessage: "RuntimeError: call aclnnFoo failed, error code is 10001"},
			{NodeID: "test_dropout.py::TestDropout::test_dropout_p", FilePath: "test/nn/test_dropout.py", Status: "passed"},
			{NodeID: "test_batchnorm.py::TestBN::test_bn", FilePath: "test/nn/test_batchnorm.py", Status: "skipped", ErrorMessage: "Skipped: Only runs on cuda devices"},
			{NodeID: "test_tensorexpr.py::TestTE::test_te", FilePath: "test/test_tensorexpr.py", Status: "failed", ErrorMessage: "AssertionError: Tensor-likes are not close!"},
		},
		FileResults: []*models.TestFileResult{
			{RunID: 123, FilePath: "test/nn/test_dropout.py", TestType: "core", TotalCases: 2},
			{RunID: 123, FilePath: "test/nn/test_batchnorm.py", TestType: "core", TotalCases: 1},
			{RunID: 123, FilePath: "test/zero_cases.py", TestType: "core", TotalCases: 0},
		},
		Skipped: []*models.SkippedCase{
			{RunID: 123, NodeID: "test/nn/test_init.py::TestNNInit::test_orthogonal", FilePath: "test/nn/test_init.py", SkipReason: "PyTorch compiled without Lapack", SkipCategory: "", SkipSource: "running_skiped_testcases.json"},
			{RunID: 123, NodeID: "test/nn/test_convolution.py::TestConv::test_conv2d", FilePath: "test/nn/test_convolution.py", SkipReason: "not implemented for DT_COMPLEX", SkipCategory: "device not supported", SkipSource: "disabled_testcases.json"},
		},
	}
}

func generateForTest(t *testing.T, in Input) string {
	t.Helper()
	confDir := t.TempDir()
	writeTestConf(t, confDir)
	result, err := GenerateReports(in, t.TempDir(), os.DirFS(confDir))
	if err != nil {
		t.Fatalf("GenerateReports: %v", err)
	}
	return filepath.Dir(result.ZipPath)
}

func TestGenerateReportsProducesZip(t *testing.T) {
	workDir := generateForTest(t, testInput())
	zipPath := filepath.Join(workDir, "report.zip")

	info, err := os.Stat(zipPath)
	if err != nil {
		t.Fatalf("zip not created: %v", err)
	}
	if info.Size() <= 0 {
		t.Error("zip size <= 0")
	}

	zipBytes, _ := os.ReadFile(zipPath)
	wantFiles := []string{
		"all_testcases.xlsx", "failed_testcases.xlsx", "summary_report.xlsx",
		"all_testcases_report.md", "disabled_testcases.json", "running_skiped_testcases.json",
	}
	for _, name := range wantFiles {
		if !strings.Contains(string(zipBytes), name) {
			t.Errorf("zip should contain %s", name)
		}
	}
}

func TestGenerateReportsWithoutSkipped(t *testing.T) {
	in := testInput()
	in.Skipped = nil
	workDir := generateForTest(t, in)
	for _, name := range []string{"disabled_testcases.json", "running_skiped_testcases.json"} {
		if _, err := os.Stat(filepath.Join(workDir, name)); err != nil {
			t.Errorf("%s should exist even with empty skipped: %v", name, err)
		}
	}
}

func TestGenerateReportsFailsWithoutCategories(t *testing.T) {
	confDir := t.TempDir()
	outDir := t.TempDir()
	os.WriteFile(filepath.Join(confDir, "ops.txt"), []byte("aclnnAdd\n"), 0644)
	os.WriteFile(filepath.Join(confDir, "_summary_template_data.json"), []byte(testSummaryTemplate()), 0644)

	if _, err := GenerateReports(testInput(), outDir, os.DirFS(confDir)); err == nil {
		t.Error("expected error when failed_categories.json missing")
	}
}

func TestGenerateReportsFailsWithoutSummaryTemplate(t *testing.T) {
	confDir := t.TempDir()
	outDir := t.TempDir()
	os.WriteFile(filepath.Join(confDir, "ops.txt"), []byte("aclnnAdd\n"), 0644)
	cats, _ := os.ReadFile(filepath.Join("..", "..", "conf", "failed_categories.json"))
	os.WriteFile(filepath.Join(confDir, "failed_categories.json"), cats, 0644)

	if _, err := GenerateReports(testInput(), outDir, os.DirFS(confDir)); err == nil {
		t.Error("expected error when summary template missing")
	}
}

func TestGenerateReportsFailsOnCorruptSummaryTemplate(t *testing.T) {
	confDir := t.TempDir()
	outDir := t.TempDir()
	os.WriteFile(filepath.Join(confDir, "ops.txt"), []byte("aclnnAdd\n"), 0644)
	os.WriteFile(filepath.Join(confDir, "_summary_template_data.json"), []byte("{not valid json"), 0644)
	cats, _ := os.ReadFile(filepath.Join("..", "..", "conf", "failed_categories.json"))
	os.WriteFile(filepath.Join(confDir, "failed_categories.json"), cats, 0644)

	if _, err := GenerateReports(testInput(), outDir, os.DirFS(confDir)); err == nil {
		t.Error("expected error when summary template corrupt")
	}
}

func TestAllTestcasesNineColumnsAndSheets(t *testing.T) {
	workDir := generateForTest(t, testInput())
	f, err := excelize.OpenFile(filepath.Join(workDir, "all_testcases.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	// 首现序：dropout(Core/NN 先出现) -> Core sheet；tensorexpr 未匹配 -> Other 最后；黑名单跳过末尾
	wantSheets := []string{"all_files", "all_testcases", "Core", "Other", "黑名单跳过"}
	if len(sheets) != len(wantSheets) {
		t.Fatalf("sheets = %v, want %v", sheets, wantSheets)
	}
	for i, s := range wantSheets {
		if sheets[i] != s {
			t.Fatalf("sheet order = %v, want %v", sheets, wantSheets)
		}
	}

	// 9 列表头
	header := []string{"Classification", "Specialization", "File", "nodeid", "执行结果", "报错日志", "skip原生日志首行", "不支持", "不支持原因"}
	for i, h := range header {
		got, _ := f.GetCellValue("all_testcases", cellName(i, 1))
		if got != h {
			t.Errorf("all_testcases header col %d = %q, want %q", i, got, h)
		}
	}

	// F/G 列状态条件：passed 行 F/G 为空；skipped 行 G 为首行
	rows, _ := f.GetRows("all_testcases")
	if len(rows) != 5 {
		t.Fatalf("all_testcases should have 4 data rows, got %d", len(rows)-1)
	}
	getVal := func(col string, row int) string {
		v, _ := f.GetCellValue("all_testcases", fmt.Sprintf("%s%d", col, row))
		return v
	}
	for row := 2; row <= 5; row++ {
		nodeid := getVal("D", row)
		switch nodeid {
		case "test_dropout.py::TestDropout::test_dropout_p":
			if getVal("F", row) != "" || getVal("G", row) != "" {
				t.Errorf("passed row F/G should be empty, got F=%q G=%q", getVal("F", row), getVal("G", row))
			}
		case "test_batchnorm.py::TestBN::test_bn":
			if getVal("G", row) != "Skipped: Only runs on cuda devices" {
				t.Errorf("skipped row G = %q, want skip first line", getVal("G", row))
			}
		}
	}

	// 分类 sheet 的 A/B：模板映射 NN（计数剥离）；未匹配 Other/Other
	cls, _ := f.GetCellValue("Core", "A2")
	spec, _ := f.GetCellValue("Core", "B2")
	if cls != "Core" || spec != "NN" {
		t.Errorf("Core sheet A2/B2 = %q/%q, want Core/NN", cls, spec)
	}
	ocls, _ := f.GetCellValue("Other", "A2")
	ospec, _ := f.GetCellValue("Other", "B2")
	if ocls != "Other" || ospec != "Other" {
		t.Errorf("Other sheet A2/B2 = %q/%q, want Other/Other", ocls, ospec)
	}

	// 分类 sheet C 列不合并；A/B joint 合并（同 (Core,NN) 3 行 → A2:A4 合并）
	merges, _ := f.GetMergeCells("Core")
	mergedRefs := map[string]bool{}
	for _, m := range merges {
		mergedRefs[m.GetStartAxis()+"-"+m.GetEndAxis()] = true
	}
	if !mergedRefs["A2-A4"] || !mergedRefs["B2-B4"] {
		t.Errorf("Core sheet should merge A2:A4 and B2:B4 (joint), got %v", mergedRefs)
	}
	for _, m := range merges {
		if strings.HasPrefix(m.GetStartAxis(), "C") {
			t.Errorf("classification sheet should not merge C column, got %v", m.GetStartAxis())
		}
	}
}

func TestAllFilesSheetJointMerge(t *testing.T) {
	workDir := generateForTest(t, testInput())
	f, err := excelize.OpenFile(filepath.Join(workDir, "all_testcases.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// 排序后：Core/Autograd(zero_cases 未匹配->Other) ... 检查 joint 合并：同 (cls,spec) 相邻行合并 A/B
	rows, _ := f.GetRows("all_files")
	if len(rows) != 4 {
		t.Fatalf("all_files should have 3 data rows, got %d", len(rows)-1)
	}
	// test/nn/test_dropout.py 与 test_batchnorm 同为 (Core,NN) → A/B 合并为一组
	merges, _ := f.GetMergeCells("all_files")
	var mergedAB bool
	for _, m := range merges {
		if m.GetStartAxis() == "A2" && m.GetEndAxis() == "A3" {
			mergedAB = true
		}
	}
	if !mergedAB {
		t.Errorf("all_files should merge A2:A3 for same (cls,spec), merges=%v", merges)
	}
	// num 列
	numByFile := map[string]string{}
	for _, row := range rows[1:] {
		if len(row) >= 4 {
			numByFile[row[2]] = row[3]
		}
	}
	if numByFile["test/nn/test_dropout.py"] != "2" || numByFile["test/zero_cases.py"] != "0" {
		t.Errorf("all_files num = %v", numByFile)
	}
}

func TestSkipFirstLineOnlyForSkipped(t *testing.T) {
	orderedFiles := []fileGroup{{
		path: "test/nn/test_skip.py",
		cases: []TestCaseInfo{
			{NodeID: "t_skip", Status: "skipped", Message: "Skipped: reason A\nline2"},
			{NodeID: "t_error", Status: "error", Message: "RuntimeError: boom\nline2"},
			{NodeID: "t_failed", Status: "failed", Message: "AssertionError: x\nline2"},
		},
	}}
	path := filepath.Join(t.TempDir(), "all_testcases.xlsx")
	if err := writeAllTestcases(path, orderedFiles, FileMap{}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if got, _ := f.GetCellValue("Other", "G2"); got != "Skipped: reason A" {
		t.Errorf("skipped G2 = %q, want %q", got, "Skipped: reason A")
	}
	if got, _ := f.GetCellValue("Other", "G3"); got != "" {
		t.Errorf("error G3 = %q, want empty", got)
	}
	if got, _ := f.GetCellValue("Other", "G4"); got != "" {
		t.Errorf("failed G4 = %q, want empty", got)
	}
	// F 列：failed/error/skipped 都写 message
	if got, _ := f.GetCellValue("Other", "F3"); got != "RuntimeError: boom\nline2" {
		t.Errorf("error F3 = %q, want message", got)
	}
}

func TestFailedWorkbookAllFailedSheet(t *testing.T) {
	workDir := generateForTest(t, testInput())
	f, err := excelize.OpenFile(filepath.Join(workDir, "failed_testcases.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 || sheets[0] != "all_failed" {
		t.Fatalf("expected first sheet all_failed, got %v", sheets)
	}

	rows, _ := f.GetRows("all_failed")
	// 2 条失败（aclnn + assertion），无 unsupported 排除
	if len(rows) != 3 {
		t.Errorf("all_failed should have 2 data rows, got %d", len(rows)-1)
	}
	cats := map[string]bool{}
	for _, row := range rows[1:] {
		if len(row) >= 7 {
			cats[row[6]] = true
		}
	}
	if !cats["aclnn调用失败"] || !cats["断言失败-数值精度不匹配"] {
		t.Errorf("failure categories = %v", cats)
	}
}

func TestFailedGroupSheetNameWithSpecCount(t *testing.T) {
	// 模板 B 值含全角计数（Python 不剥离），分组名 = cls+spec（含全角）
	tmpl := `{"sheets": {"Core": {"cells": {
		"A2": {"value": "Core"}, "B2": {"value": "Optim （6）"},
		"C3": {"value": "test/optim/test_x.py"}
	}}}}`
	var t2 SummaryTemplate
	if err := loadTemplateJSON(t, tmpl, &t2); err != nil {
		t.Fatal(err)
	}
	fm := BuildFileMap(&t2)
	fc := classifyFile(fm, "test/optim/test_x.py")
	if fc.Classification != "Core" || fc.Specialization != "Optim （6）" {
		t.Fatalf("classifyFile = %+v, want Core/Optim （6）", fc)
	}
}

func loadTemplateJSON(t *testing.T, data string, out *SummaryTemplate) error {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "_summary_template_data.json")
	if err := os.WriteFile(p, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSummaryTemplate(p)
	if err != nil {
		return err
	}
	*out = *loaded
	return nil
}

func TestBuildFileMapMergeAndHeaderSkip(t *testing.T) {
	tmpl := `{"sheets": {
		"README": {"cells": {"A2": {"value": "Core"}, "C3": {"value": "readme_file.py"}}},
		"Core": {"cells": {
			"A1": {"value": "Classification"}, "B1": {"value": "Specialization"}, "C1": {"value": "File"},
			"A2": {"value": "Core"}, "B2": {"value": "NN(22)"},
			"C2": {"value": "test/nn/a.py"},
			"C3": {"value": "test/nn/b.py"},
			"B4": {"value": "Autograd (11)"},
			"C4": {"value": "test/autograd/c.py"},
			"C5": {"value": "test/autograd/d.py"}
		}}
	}}`
	var t2 SummaryTemplate
	if err := loadTemplateJSON(t, tmpl, &t2); err != nil {
		t.Fatal(err)
	}
	fm := BuildFileMap(&t2)
	if len(fm) != 4 {
		t.Fatalf("fileMap size = %d, want 4 (README skipped, header row skipped)", len(fm))
	}
	if fc := fm["test/nn/a.py"]; fc.Classification != "Core" || fc.Specialization != "NN" {
		t.Errorf("a.py = %+v, want Core/NN", fc)
	}
	if fc := fm["test/autograd/c.py"]; fc.Classification != "Core" || fc.Specialization != "Autograd" {
		t.Errorf("c.py = %+v, want Core/Autograd (cascade + count strip)", fc)
	}
	if fc := fm["test/autograd/d.py"]; fc.Specialization != "Autograd" {
		t.Errorf("d.py = %+v, want Specialization cascade", fc)
	}
}

func TestBlacklistSheetAndJSONs(t *testing.T) {
	workDir := generateForTest(t, testInput())

	f, err := excelize.OpenFile(filepath.Join(workDir, "all_testcases.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	found := false
	for _, s := range f.GetSheetList() {
		if s == "黑名单跳过" {
			found = true
		}
	}
	if !found {
		t.Fatalf("all_testcases.xlsx should contain 黑名单跳过 sheet, got %v", f.GetSheetList())
	}

	rows, _ := f.GetRows("黑名单跳过")
	if len(rows) != 3 {
		t.Fatalf("黑名单跳过 should have 2 data rows, got %d", len(rows)-1)
	}

	catBySource := map[string]string{}
	for _, row := range rows[1:] {
		if len(row) >= 6 {
			catBySource[row[4]] = row[5]
		}
	}
	if catBySource["running_skiped_testcases.json"] != "Running Skiped" {
		t.Errorf("running_skiped empty category should be 'Running Skiped', got %q", catBySource["running_skiped_testcases.json"])
	}
	if catBySource["disabled_testcases.json"] != "device not supported" {
		t.Errorf("disabled category mismatch: %q", catBySource["disabled_testcases.json"])
	}

	disabledData, err := os.ReadFile(filepath.Join(workDir, "disabled_testcases.json"))
	if err != nil {
		t.Fatalf("disabled_testcases.json missing: %v", err)
	}
	if !strings.Contains(string(disabledData), "test/nn/test_convolution.py::TestConv::test_conv2d") {
		t.Error("disabled_testcases.json should contain conv2d case from skipped input")
	}
	runningData, err := os.ReadFile(filepath.Join(workDir, "running_skiped_testcases.json"))
	if err != nil {
		t.Fatalf("running_skiped_testcases.json missing: %v", err)
	}
	if !strings.Contains(string(runningData), "test/nn/test_init.py::TestNNInit::test_orthogonal") {
		t.Error("running_skiped_testcases.json should contain test_orthogonal from skipped input")
	}
}

// TestBlacklistJSONOrderAndFormat 键序=插入序（skipped 序 + 增量序），
// 字节格式对齐 Python _write_disabled_json：首行 `{`、4 空格缩进、末条无逗号、尾 `}\n`。
func TestBlacklistJSONOrderAndFormat(t *testing.T) {
	in := testInput()
	in.Skipped = []*models.SkippedCase{
		{RunID: 123, NodeID: "zzz/nodeid.py::TestA::t1", FilePath: "zzz/nodeid.py", SkipReason: "r1", SkipCategory: "c1", SkipSource: "disabled_testcases.json"},
		{RunID: 123, NodeID: "aaa/nodeid.py::TestB::t2", FilePath: "aaa/nodeid.py", SkipReason: "r2", SkipCategory: "", SkipSource: "disabled_testcases.json"},
	}
	in.Cases = append(in.Cases, &models.TestCase{
		NodeID: "mmm/incr.py::TestC::t3", FilePath: "test/nn/test_dropout.py",
		Status: "failed", ErrorMessage: "RuntimeError: not implemented for DT_DOUBLE",
	})
	workDir := generateForTest(t, in)

	data, err := os.ReadFile(filepath.Join(workDir, "disabled_testcases.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	// 键序：zzz 在前（skipped 插入序），aaa 其次，增量 mmm 最后（非字母序）
	zzzIdx := strings.Index(content, `"zzz/nodeid.py::TestA::t1"`)
	aaaIdx := strings.Index(content, `"aaa/nodeid.py::TestB::t2"`)
	mmmIdx := strings.Index(content, `"mmm/incr.py::TestC::t3"`)
	if !(zzzIdx >= 0 && aaaIdx > zzzIdx && mmmIdx > aaaIdx) {
		t.Errorf("disabled json key order should be insertion order, content:\n%s", content)
	}
	// 增量条目：category 来自 typeMap，issue 空
	if !strings.Contains(content, `"mmm/incr.py::TestC::t3": {"category": "dtype_not_supported", "reason": "not implemented for DT_DOUBLE", "issue": ""}`) {
		t.Errorf("incremental entry format mismatch:\n%s", content)
	}
	// 字节级格式
	if !strings.HasPrefix(content, "{\n") || !strings.HasSuffix(content, "}\n") {
		t.Errorf("json frame mismatch: %q...%q", content[:4], content[len(content)-4:])
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	lastEntry := lines[len(lines)-2]
	if strings.HasSuffix(lastEntry, ",") {
		t.Errorf("last entry should have no trailing comma: %q", lastEntry)
	}
	if !strings.HasPrefix(lastEntry, "    ") {
		t.Errorf("entries should be 4-space indented: %q", lastEntry)
	}
}

// TestBlacklistIncrementalOpOnly 增量 disabled 须含 op 命中（非仅 pattern），
// op-only 条目 category 为空串。
func TestBlacklistIncrementalOpOnly(t *testing.T) {
	in := testInput()
	in.Skipped = nil
	in.Cases = []*models.TestCase{
		{NodeID: "op_only.py::T::t", FilePath: "test/nn/test_dropout.py", Status: "failed", ErrorMessage: "RuntimeError: call aclnnAdd failed"},
	}
	workDir := generateForTest(t, in)

	data, err := os.ReadFile(filepath.Join(workDir, "disabled_testcases.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `"op_only.py::T::t": {"category": "", "reason": "aclnnAdd", "issue": ""}`) {
		t.Errorf("op-only incremental entry should exist with empty category:\n%s", content)
	}
}

func TestMarkdownReportSections(t *testing.T) {
	workDir := generateForTest(t, testInput())

	data, err := os.ReadFile(filepath.Join(workDir, "all_testcases_report.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)

	if !strings.HasPrefix(md, "# 测试报告\n") {
		t.Errorf("markdown title should be '# 测试报告', got %q", md[:20])
	}
	for _, section := range []string{"## 总体概览", "## 执行结果分布", "## 失败用例分类", "## 黑名单用例统计"} {
		if !strings.Contains(md, section) {
			t.Errorf("markdown should contain %s", section)
		}
	}
	// 旧版章节不应存在
	for _, gone := range []string{"## 不支持用例统计", "## 关键问题"} {
		if strings.Contains(md, gone) {
			t.Errorf("markdown should NOT contain %s", gone)
		}
	}
	if !strings.Contains(md, "aclnn调用失败") {
		t.Error("markdown failure categories should include aclnn调用失败")
	}
	if !strings.Contains(md, "Running Skiped") {
		t.Error("markdown blacklist stats should include Running Skiped")
	}
}

func TestMarkdownStableSortOnTie(t *testing.T) {
	// 构造两个同计数的分类：首现序 "later-cat" 在前，稳定排序应保持
	in := Input{
		RunID: 1,
		Cases: []*models.TestCase{
			{NodeID: "a.py::T::t1", FilePath: "test/nn/test_dropout.py", Status: "failed", ErrorMessage: "AttributeError: 'Tensor' object has no attribute 'foo'"},
			{NodeID: "a.py::T::t2", FilePath: "test/nn/test_dropout.py", Status: "failed", ErrorMessage: "CppCompileError: C++ compile error"},
		},
	}
	workDir := generateForTest(t, in)
	data, err := os.ReadFile(filepath.Join(workDir, "all_testcases_report.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	attrIdx := strings.Index(md, "其他-属性缺失")
	compileIdx := strings.Index(md, "其他-编译错误")
	if attrIdx < 0 || compileIdx < 0 {
		t.Fatalf("both categories should appear:\n%s", md)
	}
	if attrIdx > compileIdx {
		t.Errorf("tie should keep first-appearance order (属性缺失 before 编译错误)")
	}
}

func TestMarkdownNoBlacklistSectionWhenEmpty(t *testing.T) {
	in := testInput()
	in.Skipped = nil
	workDir := generateForTest(t, in)

	data, err := os.ReadFile(filepath.Join(workDir, "all_testcases_report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "## 黑名单用例统计") {
		t.Error("markdown should omit blacklist section when no skipped cases")
	}
}

func TestMarkdownEmptyCasesNotice(t *testing.T) {
	workDir := generateForTest(t, Input{RunID: 999})
	data, err := os.ReadFile(filepath.Join(workDir, "all_testcases_report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "无测试用例数据") {
		t.Error("markdown should contain data-unavailable notice for empty cases")
	}

	workDir2 := generateForTest(t, testInput())
	data2, _ := os.ReadFile(filepath.Join(workDir2, "all_testcases_report.md"))
	if strings.Contains(string(data2), "无测试用例数据") {
		t.Error("markdown with cases should NOT contain the notice")
	}
}

func TestGenerateReportsDeterministic(t *testing.T) {
	in := testInput()
	workDir1 := generateForTest(t, in)
	workDir2 := generateForTest(t, in)
	for _, name := range []string{
		"all_testcases_report.md", "disabled_testcases.json", "running_skiped_testcases.json",
	} {
		b1, err := os.ReadFile(filepath.Join(workDir1, name))
		if err != nil {
			t.Fatal(err)
		}
		b2, err := os.ReadFile(filepath.Join(workDir2, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(b1) != string(b2) {
			t.Errorf("%s should be deterministic across runs", name)
		}
	}
}

func TestSafeSheetName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Tensor Operators+Type & Schema", "Tensor Operators+Type & Schema"}, // 30 码点不截断
		{"Core+Optim （6）", "Core+Optim （6）"},                       // 全角按码点计数
		{strings.Repeat("a", 31), strings.Repeat("a", 31)},              // 31 不截断
		{strings.Repeat("a", 32), strings.Repeat("a", 31)},              // 32 截到 31
		{strings.Repeat("中", 32), strings.Repeat("中", 31)},              // rune 截断不切坏 UTF-8
	}
	for _, c := range cases {
		if got := safeSheetName(c.in); got != c.want {
			t.Errorf("safeSheetName(%q) = %q (len %d), want %q (len %d)",
				c.in, got, len(got), c.want, len(c.want))
		}
	}
}

func TestGenerateWithRealConf(t *testing.T) {
	confDir := filepath.Join("..", "..", "conf")
	outDir := t.TempDir()

	if _, err := os.Stat(filepath.Join(confDir, "_summary_template_data.json")); os.IsNotExist(err) {
		t.Skip("real summary template not found in conf/")
	}

	result, err := GenerateReports(testInput(), outDir, os.DirFS(confDir))
	if err != nil {
		t.Fatalf("GenerateReports with real conf: %v", err)
	}
	if result.FileCount != 6 {
		t.Errorf("expected 6 files, got %d", result.FileCount)
	}
	if result.TotalSize <= 0 {
		t.Error("total size should be > 0")
	}
}
