package report

import (
	"regexp"
	"strings"
)

func checkOpsInMessage(message string, ops []string) string {
	if message == "" || len(ops) == 0 {
		return ""
	}
	for _, op := range ops {
		if strings.Contains(message, op) {
			return op
		}
	}
	return ""
}

// extractFirstLine 提取报错日志首行，对齐 Python message.split("\n")[0].strip()
// （先取首行再 TrimSpace，不截断）。
func extractFirstLine(message string) string {
	if message == "" {
		return ""
	}
	firstLine := message
	if idx := strings.IndexByte(message, '\n'); idx >= 0 {
		firstLine = message[:idx]
	}
	return strings.TrimSpace(firstLine)
}

// classifyCase 返回 (isUnsupported, reason)，对齐 Python fill_cdef H/I 列：
// 仅 failed/error 判定；unsupported 模式优先（大小写敏感），其次算子命中。
func classifyCase(status, message string, ops []string, unsupported *UnsupportedConfig) (string, string) {
	if status != "failed" && status != "error" {
		return "", ""
	}
	if pattern := unsupported.checkUnsupportedPattern(message); pattern != "" {
		return "是", pattern
	}
	if op := checkOpsInMessage(message, ops); op != "" {
		return "是", op
	}
	return "", ""
}

var (
	reNoiseTimestamp = []*regexp.Regexp{
		regexp.MustCompile(`\[ERROR\]\s*\d{4}-\d{2}-\d{2}-\d{2}:\d{2}:\d{2}\s*\(PID:\d+,\s*Device:\d+,\s*RankID:[-0-9]+\)\s*`),
		regexp.MustCompile(`\[PID:\s*\d+\]\s*\d{4}-\d{2}-\d{2}-\d{2}:\d{2}:\d{2}(?:\.\d+)*\s*`),
		regexp.MustCompile(`\d{4}-\d{2}-\d{2}-\d{2}:\d{2}:\d{2}(?:\.\d+)*\s*`),
		regexp.MustCompile(`(?m)^\s*E\s+`),
	}
	reWhitespace = regexp.MustCompile(`\s+`)

	reExtractMessages = []*regexp.Regexp{
		regexp.MustCompile(`RuntimeError: (.+)`),
		regexp.MustCompile(`AssertionError: (.+)`),
		regexp.MustCompile(`AttributeError: (.+)`),
		regexp.MustCompile(`IndexError: (.+)`),
		regexp.MustCompile(`KeyError: (.+)`),
		regexp.MustCompile(`TypeError: (.+)`),
		regexp.MustCompile(`ValueError: (.+)`),
		regexp.MustCompile(`Exception: (.+)`),
		regexp.MustCompile(`NotImplementedError: (.+)`),
		regexp.MustCompile(`CppCompileError: (.+)`),
		regexp.MustCompile(`BackendCompilerFailed: (.+)`),
	}
)

func cleanEvidence(s string, maxLen int) string {
	if s == "" {
		return ""
	}
	for _, pat := range reNoiseTimestamp {
		s = pat.ReplaceAllString(s, "")
	}
	s = strings.TrimSpace(reWhitespace.ReplaceAllString(s, " "))
	s = strings.TrimRight(s, ":; \t")
	if maxLen > 0 {
		if r := []rune(s); len(r) > maxLen {
			s = string(r[:maxLen])
		}
	}
	return s
}

func extractRuntimeMessage(text string) string {
	for _, pat := range reExtractMessages {
		m := pat.FindStringSubmatch(text)
		if m != nil {
			msg := m[1]
			if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
				msg = msg[:idx]
			}
			msg = strings.TrimSpace(msg)
			return cleanEvidence(msg, 100)
		}
	}
	return ""
}
