package report

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"pytorch-cicd-analysis/internal/models"
)

// writeMarkdownReport 生成 all_testcases_report.md，对齐 /tmp/20260827 gen_report.py：
// 标题「# 测试报告」+ 总体概览/执行结果分布/失败用例分类/黑名单用例统计 四章。
// 计数收集按首现顺序，排序用稳定降序（对齐 Python Counter 首现序 + sorted 稳定性）。
func writeMarkdownReport(path string, orderedFiles []fileGroup, fileMap FileMap, ops []string, failedCatCounts []countKV, skipped []*models.SkippedCase, unsupportedCfg *UnsupportedConfig) error {
	type sheetStat struct {
		total       int
		passed      int
		statusCount map[string]int
		unsupported int
	}

	stats := map[string]*sheetStat{}
	getStat := func(cls string) *sheetStat {
		if st, ok := stats[cls]; ok {
			return st
		}
		st := &sheetStat{statusCount: map[string]int{}}
		stats[cls] = st
		return st
	}

	for _, fg := range orderedFiles {
		fc := classifyFile(fileMap, fg.path)
		st := getStat(fc.Classification)
		for _, tc := range fg.cases {
			st.total++
			st.statusCount[tc.Status]++
			if tc.Status == "passed" {
				st.passed++
			}
			unsupported, _ := classifyCase(tc.Status, tc.Message, ops, unsupportedCfg)
			if unsupported == "是" {
				st.unsupported++
			}
		}
	}

	var lines []string
	lines = append(lines, "# 测试报告", "")

	if len(orderedFiles) == 0 {
		lines = append(lines, "> ⚠️ 该 run 无测试用例数据（summary 制品缺失或已过期）", "")
	}

	// 总体概览（python 口径：按 sheet 名字母序；总用例/passed/failed/errors/skipped/通过率）
	sheetNames := make([]string, 0, len(stats))
	for name := range stats {
		sheetNames = append(sheetNames, name)
	}
	sort.Strings(sheetNames)

	lines = append(lines, "## 总体概览", "")
	lines = append(lines, "| 分类 | 总用例 | passed | failed | errors | skipped | 通过率 |")
	lines = append(lines, "|---|---:|---:|---:|---:|---:|---:|")
	var sumTotal, sumPassed, sumFailed, sumErrors, sumSkipped int
	for _, name := range sheetNames {
		st := stats[name]
		if st.total == 0 {
			continue
		}
		failed := st.statusCount["failed"]
		errs := st.statusCount["error"]
		sk := st.statusCount["skipped"]
		rate := float64(st.passed) / float64(st.total) * 100
		lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %.1f%% |",
			name, comma(st.total), comma(st.passed), comma(failed), comma(errs), comma(sk), rate))
		sumTotal += st.total
		sumPassed += st.passed
		sumFailed += failed
		sumErrors += errs
		sumSkipped += sk
	}
	sumRate := 0.0
	if sumTotal > 0 {
		sumRate = float64(sumPassed) / float64(sumTotal) * 100
	}
	lines = append(lines, fmt.Sprintf("| **合计** | **%s** | **%s** | **%s** | **%s** | **%s** | **%.1f%%** |",
		comma(sumTotal), comma(sumPassed), comma(sumFailed), comma(sumErrors), comma(sumSkipped), sumRate))
	lines = append(lines, "")

	// 执行结果分布（python 口径：total_valid = p+f+e+sk；other_failed = f+e-unsupported）
	totalValid := sumPassed + sumFailed + sumErrors + sumSkipped
	totalUnsupported := 0
	for _, st := range stats {
		totalUnsupported += st.unsupported
	}
	otherFailed := sumFailed + sumErrors - totalUnsupported

	pct := func(v int) string {
		if totalValid == 0 {
			return "0.0%"
		}
		return fmt.Sprintf("%.1f%%", float64(v)/float64(totalValid)*100)
	}

	lines = append(lines, "## 执行结果分布", "")
	lines = append(lines, "| 状态 | 数量 | 占比 |")
	lines = append(lines, "|---|---:|---:|")
	lines = append(lines, fmt.Sprintf("| passed | %s | %s |", comma(sumPassed), pct(sumPassed)))
	lines = append(lines, fmt.Sprintf("| failed（不支持） | %s | %s |", comma(totalUnsupported), pct(totalUnsupported)))
	lines = append(lines, fmt.Sprintf("| failed（其他） | %s | %s |", comma(otherFailed), pct(otherFailed)))
	lines = append(lines, fmt.Sprintf("| skipped | %s | %s |", comma(sumSkipped), pct(sumSkipped)))
	lines = append(lines, "")

	// 失败用例分类（计数来自 failed_testcases.xlsx 生成，口径一致：已排除 unsupported）
	if len(failedCatCounts) > 0 {
		totalFailed := 0
		for _, c := range failedCatCounts {
			totalFailed += c.Count
		}
		sorted := make([]countKV, len(failedCatCounts))
		copy(sorted, failedCatCounts)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Count > sorted[j].Count })

		lines = append(lines, fmt.Sprintf("## 失败用例分类（共 %s 条）", comma(totalFailed)), "")
		lines = append(lines, "| 失败大类 | 数量 | 占比 |")
		lines = append(lines, "|---|---:|---:|")
		for _, c := range sorted {
			lines = append(lines, fmt.Sprintf("| %s | %s | %.1f%% |",
				c.Name, comma(c.Count), float64(c.Count)/float64(totalFailed)*100))
		}
		lines = append(lines, "")
	}

	// 黑名单用例统计（按 skip 分类；running_skiped 空分类补 Running Skiped；
	// 空 category 不计数，total = 计数和）
	if len(skipped) > 0 {
		catCounts := map[string]int{}
		var catOrder []string
		for _, sc := range skipped {
			category := sc.SkipCategory
			if sc.SkipSource == "running_skiped_testcases.json" && category == "" {
				category = "Running Skiped"
			}
			if category == "" {
				continue
			}
			if _, ok := catCounts[category]; !ok {
				catOrder = append(catOrder, category)
			}
			catCounts[category]++
		}
		totalBl := 0
		for _, c := range catCounts {
			totalBl += c
		}

		if totalBl > 0 {
			sorted := make([]countKV, 0, len(catOrder))
			for _, name := range catOrder {
				sorted = append(sorted, countKV{name, catCounts[name]})
			}
			sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Count > sorted[j].Count })

			lines = append(lines, fmt.Sprintf("## 黑名单用例统计（共 %s 条）", comma(totalBl)), "")
			lines = append(lines, "### 按 skip 分类")
			lines = append(lines, "")
			lines = append(lines, "| skip分类 | 数量 | 占比 |")
			lines = append(lines, "|---|---:|---:|")
			for _, c := range sorted {
				lines = append(lines, fmt.Sprintf("| %s | %s | %.1f%% |",
					c.Name, comma(c.Count), float64(c.Count)/float64(totalBl)*100))
			}
			lines = append(lines, "")
		}
	}

	content := strings.Join(lines, "\n")
	return os.WriteFile(path, []byte(content), 0644)
}

func comma(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
