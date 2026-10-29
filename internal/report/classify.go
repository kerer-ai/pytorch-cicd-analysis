package report

import (
	"regexp"
	"strings"
)

// checkOpsInMessage 返回 message 中真实调用失败的算子名。
// 仅当日志包含 "call <op> failed" 形式（如 "call aclnnAdd failed"）才视为真实
// 调用该算子，避免仅因日志提及算子名而误命中；并取最长匹配，避免短算子名
// （如 aclnnAdd）抢先匹配到长算子名（如 aclnnAddmm）的前缀，无需依赖 ops.txt 排序。
func checkOpsInMessage(message string, ops []string) string {
	if message == "" || len(ops) == 0 {
		return ""
	}
	var best string
	for _, op := range ops {
		if len(op) > len(best) && strings.Contains(message, "call "+op+" failed") {
			best = op
		}
	}
	return best
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
// 仅 failed/error 判定；仅 unsupported 模式命中（大小写敏感）标记为不支持。
// 算子命中（ops.txt）不再标记为不支持——改为在 failed_testcases 单独输出「算子失败用例」。
func classifyCase(status, message string, _ []string, unsupported *UnsupportedConfig) (string, string) {
	if status != "failed" && status != "error" {
		return "", ""
	}
	if pattern := unsupported.checkUnsupportedPattern(message); pattern != "" {
		return "是", pattern
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
