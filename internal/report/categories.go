package report

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Rule struct {
	Name                string   `json:"name"`
	Regex               string   `json:"regex"`
	Keyword             string   `json:"keyword"`
	AllKeywords         []string `json:"all_keywords"`
	KeywordsAny         []string `json:"keywords_any"`
	KeywordsAll         []string `json:"keywords_all"`
	KeywordsAllRequired []string `json:"keywords_all_required"`
	KeywordsAllAlt      []string `json:"keywords_all_alt"`
	ExtraKeywords       []string `json:"extra_keywords"`
	FallbackKeywords    []string `json:"fallback_keywords"`
	Evidence            string   `json:"evidence"`

	compiled *regexp.Regexp
}

type Category struct {
	Name            string   `json:"name"`
	Rules           []Rule   `json:"rules"`
	Subcategories   []Rule   `json:"subcategories"`
	ExcludeKeywords []string `json:"exclude_keywords"`
}

type subDefault struct {
	Name     string `json:"name"`
	Evidence string `json:"evidence"`
}

type subSection struct {
	Rules   []Rule    `json:"rules"`
	Default subDefault `json:"default"`
}

type categoriesFile struct {
	Categories            []Category `json:"categories"`
	AssertionSubcategories subSection `json:"assertion_subcategories"`
	OtherSubcategories     subSection `json:"other_subcategories"`
}

type Categorizer struct {
	categories       []Category
	assertionRules   []Rule
	assertionDefault subDefault
	otherRules       []Rule
	otherDefault     subDefault
}

func NewCategorizer(fsys fs.FS, path string) (*Categorizer, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cf categoriesFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cf.Categories) == 0 {
		return nil, fmt.Errorf("%s: no categories defined", path)
	}

	compileRules := func(rules []Rule) {
		for i := range rules {
			if rules[i].Regex != "" {
				if re, err := regexp.Compile(`(?i)` + rules[i].Regex); err == nil {
					rules[i].compiled = re
				}
			}
		}
	}
	for i := range cf.Categories {
		compileRules(cf.Categories[i].Rules)
		compileRules(cf.Categories[i].Subcategories)
	}
	compileRules(cf.AssertionSubcategories.Rules)
	compileRules(cf.OtherSubcategories.Rules)

	return &Categorizer{
		categories:       cf.Categories,
		assertionRules:   cf.AssertionSubcategories.Rules,
		assertionDefault: cf.AssertionSubcategories.Default,
		otherRules:       cf.OtherSubcategories.Rules,
		otherDefault:     cf.OtherSubcategories.Default,
	}, nil
}

var (
	reAssertionTensorLikes = regexp.MustCompile(`Tensor-likes are not (close|equal)`)
	reAssertionScalars     = regexp.MustCompile(`Scalars are not (close|equal)`)
	rePlaceholder          = regexp.MustCompile(`\{(\d+)\}`)
)

func (c *Categorizer) Categorize(log string) (string, string) {
	if log == "" {
		return "其他", "空日志"
	}
	text := log
	low := strings.ToLower(log)
	runtimeMsg := extractRuntimeMessage(text)

	for _, cat := range c.categories {
		for _, rule := range cat.Rules {
			evidence, matched := matchRule(&rule, text, low)
			if !matched {
				continue
			}
			if len(cat.ExcludeKeywords) > 0 {
				excluded := false
				for _, ekw := range cat.ExcludeKeywords {
					if strings.Contains(low, strings.ToLower(ekw)) {
						excluded = true
						break
					}
				}
				if excluded {
					continue
				}
			}
			if len(cat.Subcategories) > 0 {
				return matchSubcategory(cat.Subcategories, text, low, runtimeMsg, cat.Name)
			}
			return cat.Name, evidence
		}
	}

	isAssertion := strings.Contains(text, "AssertionError") ||
		strings.Contains(text, "GradcheckError") ||
		(strings.Contains(low, "jacobian") && strings.Contains(low, "mismatch")) ||
		reAssertionTensorLikes.MatchString(text) ||
		reAssertionScalars.MatchString(text)
	if isAssertion {
		return matchSubcategory(c.assertionRules, text, low, runtimeMsg, c.assertionDefault.Name)
	}
	return matchSubcategory(c.otherRules, text, low, runtimeMsg, c.otherDefault.Name)
}

func matchRule(rule *Rule, text, low string) (string, bool) {
	if rule.Regex != "" && rule.compiled != nil {
		m := rule.compiled.FindStringSubmatch(text)
		if m != nil {
			if rule.Evidence != "" {
				args := append([]string{m[0]}, m[1:]...)
				return formatEvidence(rule.Evidence, args, rule.Evidence), true
			}
			// 无 evidence 模板：提取匹配片段上下文（对齐 Python _extract_context(text, m.group(0))）
			return extractContext(text, m[0]), true
		}
		return "", false
	}
	if len(rule.AllKeywords) > 0 {
		all := true
		for _, kw := range rule.AllKeywords {
			if !strings.Contains(low, strings.ToLower(kw)) {
				all = false
				break
			}
		}
		if all {
			return extractContext(text, rule.AllKeywords[0]), true
		}
		return "", false
	}
	if rule.Keyword != "" {
		if strings.Contains(low, strings.ToLower(rule.Keyword)) {
			return extractContext(text, rule.Keyword), true
		}
	}
	return "", false
}

func matchSubcategory(rules []Rule, text, low, runtimeMsg, defaultName string) (string, string) {
	for i := range rules {
		rule := &rules[i]
		matched := false
		var evidence string

		if len(rule.KeywordsAny) > 0 && len(rule.KeywordsAllRequired) == 0 {
			for _, kw := range rule.KeywordsAny {
				if strings.Contains(low, strings.ToLower(kw)) {
					matched = true
					evidence = extractContext(text, kw)
					break
				}
			}
		}
		if !matched && len(rule.KeywordsAll) > 0 {
			all := true
			for _, kw := range rule.KeywordsAll {
				if !strings.Contains(low, strings.ToLower(kw)) {
					all = false
					break
				}
			}
			if all {
				matched = true
				evidence = extractContext(text, rule.KeywordsAll[0])
			}
		}
		if !matched && len(rule.KeywordsAllRequired) > 0 && len(rule.KeywordsAny) > 0 {
			allReq := true
			for _, kw := range rule.KeywordsAllRequired {
				if !strings.Contains(low, strings.ToLower(kw)) {
					allReq = false
					break
				}
			}
			if allReq {
				for _, kw := range rule.KeywordsAny {
					if strings.Contains(low, strings.ToLower(kw)) {
						matched = true
						evidence = extractContext(text, kw)
						break
					}
				}
			}
		}
		if !matched && len(rule.ExtraKeywords) > 0 {
			for _, kw := range rule.ExtraKeywords {
				if strings.Contains(low, strings.ToLower(kw)) {
					matched = true
					evidence = extractContext(text, kw)
					break
				}
			}
		}
		if !matched && rule.Regex != "" && rule.compiled != nil {
			m := rule.compiled.FindStringSubmatch(text)
			if m != nil {
				matched = true
				if rule.Evidence != "" {
					args := append([]string{m[0]}, m[1:]...)
					evidence = formatEvidence(rule.Evidence, args, rule.Evidence)
				} else {
					evidence = extractContext(text, m[0])
				}
			}
		}
		if !matched && len(rule.FallbackKeywords) > 0 {
			for _, kw := range rule.FallbackKeywords {
				if strings.Contains(low, strings.ToLower(kw)) {
					matched = true
					evidence = extractContext(text, kw)
					break
				}
			}
		}
		if !matched && rule.Keyword != "" {
			if strings.Contains(low, strings.ToLower(rule.Keyword)) {
				matched = true
				evidence = extractContext(text, rule.Keyword)
			}
		}
		if !matched && len(rule.KeywordsAllAlt) > 0 {
			all := true
			for _, kw := range rule.KeywordsAllAlt {
				if !strings.Contains(low, strings.ToLower(kw)) {
					all = false
					break
				}
			}
			if all {
				matched = true
				evidence = extractContext(text, rule.KeywordsAllAlt[0])
			}
		}

		if matched {
			name := rule.Name
			if name == "" {
				name = defaultName
			}
			// evidence 兜底链对齐 Python：evidence or runtime_msg or default_name
			if evidence == "" {
				evidence = runtimeMsg
			}
			if evidence == "" {
				evidence = defaultName
			}
			return name, evidence
		}
	}
	if runtimeMsg != "" {
		return defaultName, runtimeMsg
	}
	return defaultName, defaultName
}

// extractContext 提取 keyword 出现位置的前后上下文（按码点，对齐 Python
// _extract_context：context_len=40 字符、snippet 前后缀 "..."、cleanEvidence 120 字符）。
func extractContext(text, keyword string) string {
	idx := strings.Index(strings.ToLower(text), strings.ToLower(keyword))
	if idx < 0 {
		return keyword
	}
	runes := []rune(text)
	runeIdx := utf8.RuneCountInString(text[:idx])
	kwLen := utf8.RuneCountInString(keyword)
	const contextLen = 40
	start := runeIdx - contextLen
	if start < 0 {
		start = 0
	}
	end := runeIdx + kwLen + contextLen
	if end > len(runes) {
		end = len(runes)
	}
	snippet := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(runes) {
		snippet = snippet + "..."
	}
	return cleanEvidence(snippet, 120)
}

func formatEvidence(template string, args []string, _ string) string {
	valid := true
	out := rePlaceholder.ReplaceAllStringFunc(template, func(m string) string {
		n, err := strconv.Atoi(m[1 : len(m)-1])
		if err != nil || n >= len(args) {
			valid = false
			return m
		}
		return args[n]
	})
	if !valid {
		return template
	}
	return out
}
