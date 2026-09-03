package report

import (
	"regexp"
	"sort"
	"strings"
)

// FileMap 是 File → (Classification, Specialization) 映射，
// 由 _summary_template_data.json 模板构建（对齐 Python fill_ab_from_summary /
// load_file_map / parse_blacklist.generate_matched_blacklist 三处同构逻辑）。
type FileMap map[string]FileClass

type FileClass struct {
	Classification string
	Specialization string
}

var reASCIICount = regexp.MustCompile(`\(\d+\)`)

func stripSpecCount(s string) string {
	return strings.TrimSpace(reASCIICount.ReplaceAllString(s, ""))
}

// BuildFileMap 从 summary 模板构建 File→(Classification, Specialization) 映射：
// 跳过 README/README(Bak) sheet；跳过第 1 行表头；A/B 空值沿用上一非空值
// （合并单元格语义）；B 值去除 ASCII 计数 `(\d+)`（全角计数保留）；C 非空即记录，
// 前值缺失用 "Other" 补。
func BuildFileMap(tmpl *SummaryTemplate) FileMap {
	fm := FileMap{}
	if tmpl == nil {
		return fm
	}
	for _, entry := range tmpl.Sheets {
		if entry.Name == "README" || entry.Name == "README(Bak)" {
			continue
		}
		type rowVals struct {
			a, b, c string
			hasA    bool
		}
		rows := map[int]*rowVals{}
		for ref, cell := range entry.Sheet.Cells {
			col, row := splitCellRef(ref)
			if col == "" || row < 1 {
				continue
			}
			s, _ := cell.Value.(string)
			switch col {
			case "A":
				if s != "" {
					r := rows[row]
					if r == nil {
						r = &rowVals{}
						rows[row] = r
					}
					r.a = s
					r.hasA = true
				}
			case "B":
				if s != "" {
					r := rows[row]
					if r == nil {
						r = &rowVals{}
						rows[row] = r
					}
					r.b = s
				}
			case "C":
				if s != "" {
					r := rows[row]
					if r == nil {
						r = &rowVals{}
						rows[row] = r
					}
					r.c = s
				}
			}
		}
		rowNums := make([]int, 0, len(rows))
		for rn := range rows {
			rowNums = append(rowNums, rn)
		}
		sort.Ints(rowNums)

		lastA, lastB := "", ""
		for _, rn := range rowNums {
			if rn < 2 {
				continue
			}
			r := rows[rn]
			if r.hasA {
				lastA = r.a
			}
			if r.b != "" {
				lastB = stripSpecCount(r.b)
			}
			if r.c != "" {
				a := lastA
				if a == "" {
					a = "Other"
				}
				b := lastB
				if b == "" {
					b = "Other"
				}
				fm[r.c] = FileClass{a, b}
			}
		}
	}
	return fm
}

// classifyFile 返回文件分类，未匹配落 ("Other","Other")。
func classifyFile(fm FileMap, path string) FileClass {
	if fc, ok := fm[path]; ok {
		return fc
	}
	return FileClass{Classification: "Other", Specialization: "Other"}
}
