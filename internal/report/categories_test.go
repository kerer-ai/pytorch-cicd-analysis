package report

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func loadTestCategorizer(t *testing.T) *Categorizer {
	t.Helper()
	path := filepath.Join("..", "..", "conf", "failed_categories.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("failed_categories.json not found in conf/")
	}
	c, err := NewCategorizer(os.DirFS(filepath.Join("..", "..")), "conf/failed_categories.json")
	if err != nil {
		t.Fatalf("NewCategorizer: %v", err)
	}
	return c
}

func TestCategorizerCompile(t *testing.T) {
	c := loadTestCategorizer(t)
	if len(c.categories) == 0 {
		t.Fatal("expected categories loaded")
	}
	if len(c.categories) != 14 {
		t.Errorf("expected 14 main categories, got %d", len(c.categories))
	}
	for _, cat := range c.categories {
		for _, r := range cat.Rules {
			if r.Regex != "" && r.compiled == nil {
				t.Errorf("category %q: rule regex %q not compiled", cat.Name, r.Regex)
			}
		}
	}
}

func TestCategorizeEngineParity(t *testing.T) {
	c := loadTestCategorizer(t)

	cases := []struct {
		name string
		log  string
		cat  string
	}{
		{"empty", "", "其他"},
		{"dtype unsupported DT_DOUBLE", "RuntimeError: input dtype or format is not supported, get io input info is (DT_DOUBLE, ND)", "dtype不支持-DT_DOUBLE"},
		{"dtype mismatch", "RuntimeError: The values for attribute 'dtype' do not match", "dtype不匹配"},
		{"aclnn failed", "RuntimeError: call aclnnAdd failed, error code is 10001", "aclnn调用失败"},
		{"op not in libopapi", "RuntimeError: aclnnConv or aclnnConvGetWorkspaceSize not in libopapi.so", "算子未实现"},
		{"PTA acl api", "RuntimeError: ERR00100 operator name is aclnnRepeat", "PTA调用acl api失败"},
		{"ACL stream sync", "RuntimeError: acl stream synchronize failed", "ACL流同步失败"},
		{"LAPACK", "RuntimeError: Calling torch.linalg.eigh on a CPU tensor requires compiling PyTorch with LAPACK", "LAPACK/BLAS库缺失"},
		{"assertion numeric", "AssertionError: Tensor-likes are not close!", "断言失败-数值精度不匹配"},
		{"dtype mismatch main first", "AssertionError: dtype mismatch: float != int", "dtype不匹配"},
		{"distributed", "RuntimeError: Process 1 exited with error code 10 (HCCL communicator)", "分布式通信失败"},
		{"timeout", "RuntimeError: TimeoutError", "超时"},
		{"unexpected success", "Exception: unexpected success", "意外成功"},
		{"reshape zero", "RuntimeError: cannot reshape tensor of 0 elements into shape [2, 3]", "reshape零元素失败"},
		{"faketensor compare", "RuntimeError: when comparing the output of fake tensor", "FakeTensor比较不一致"},
		{"aclrt timeout maps to PTA", "RuntimeError: aclrtSynchronizeStreamWithTimeout timed out after 300s", "PTA调用acl api失败"},
		{"cpp compile", "CppCompileError: C++ compile error", "其他-编译错误"},
		{"attribute missing", "AttributeError: 'Tensor' object has no attribute 'foo'", "其他-属性缺失"},
		{"index out of range", "IndexError: index 5 is out of bounds for dim 0 with size 3", "其他-索引/维度越界"},
		{"keyerror", "KeyError: 'foo'", "其他-KeyError"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, evidence := c.Categorize(tc.log)
			if cat != tc.cat {
				t.Errorf("Categorize(%q) category = %q, want %q (evidence=%q)", tc.log, cat, tc.cat, evidence)
			}
		})
	}
}

func TestCategorizeEvidenceTemplates(t *testing.T) {
	c := loadTestCategorizer(t)

	// common 聚合分类：主规则 regex 命中 → 子分类 keywords_any 'call aclnn' 命中
	// evidence = 关键字上下文（extractContext，与 Python _extract_context 一致）
	cat, ev := c.Categorize("RuntimeError: call aclnnAdd failed, error code is 10001")
	if cat != "aclnn调用失败" {
		t.Fatalf("category = %q", cat)
	}
	if ev != "RuntimeError: call aclnnAdd failed, error code is 10001" {
		t.Errorf("evidence = %q, want %q", ev, "RuntimeError: call aclnnAdd failed, error code is 10001")
	}

	// 无 evidence 模板的主分类 regex：evidence = 匹配片段上下文（extractContext），
	// 非捕获组模板拼接（对齐 Python _extract_context(text, m.group(0))）
	cat, ev = c.Categorize("RuntimeError: Could not run 'aten::foo' with arguments from the 'CUDA' backend")
	if cat != "算子未实现" {
		t.Fatalf("category = %q", cat)
	}
	want := "RuntimeError: Could not run 'aten::foo' with arguments from the 'CUDA' backend"
	if ev != want {
		t.Errorf("evidence = %q, want %q", ev, want)
	}
}

func TestCategorizeEmptyLogEvidence(t *testing.T) {
	c := loadTestCategorizer(t)
	cat, ev := c.Categorize("")
	if cat != "其他" || ev != "空日志" {
		t.Errorf("Categorize(\"\") = %q, %q; want 其他, 空日志", cat, ev)
	}
}

func TestCategorizeTimeoutExcludeKeywords(t *testing.T) {
	c := loadTestCategorizer(t)

	cat, _ := c.Categorize("RuntimeError: hccl_connect_timeout set to 100")
	if cat == "超时" {
		t.Error("hccl_connect_timeout should be excluded from 超时")
	}

	cat, _ = c.Categorize("RuntimeError: communicator creation interface failed with timeout")
	if cat == "超时" {
		t.Error("communicator creation interface should be excluded from 超时")
	}
}

func TestCategorizeAssertionRegexCaseSensitive(t *testing.T) {
	c := loadTestCategorizer(t)

	cat, _ := c.Categorize("RuntimeError: tensor-likes are not close (lowercase)")
	if cat == "断言失败-数值精度不匹配" {
		t.Error("lowercase 'tensor-likes are not close' should not enter assertion branch")
	}
}

func TestNewCategorizerMissingFile(t *testing.T) {
	fsys := fstest.MapFS{}
	if _, err := NewCategorizer(fsys, "failed_categories.json"); err == nil {
		t.Error("expected error for missing config file")
	}
}

func TestNewCategorizerCorruptJSON(t *testing.T) {
	fsys := fstest.MapFS{
		"failed_categories.json": &fstest.MapFile{Data: []byte("{not valid json")},
	}
	if _, err := NewCategorizer(fsys, "failed_categories.json"); err == nil {
		t.Error("expected error for corrupt JSON")
	}
}

func TestNewCategorizerSkipsInvalidRegex(t *testing.T) {
	config := `{
		"categories": [
			{"name": "ok", "rules": [{"keyword": "boom"}]},
			{"name": "badregex", "rules": [{"regex": "(?=lookahead)", "evidence": "{0}"}]}
		],
		"assertion_subcategories": {"rules": [], "default": {"name": "断言失败-其他", "evidence": "AssertionError"}},
		"other_subcategories": {"rules": [], "default": {"name": "其他-未归类", "evidence": "未知"}}
	}`
	fsys := fstest.MapFS{
		"failed_categories.json": &fstest.MapFile{Data: []byte(config)},
	}
	c, err := NewCategorizer(fsys, "failed_categories.json")
	if err != nil {
		t.Fatalf("NewCategorizer should not fail on invalid regex: %v", err)
	}
	if len(c.categories) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(c.categories))
	}
	if c.categories[1].Rules[0].compiled != nil {
		t.Error("invalid regex rule should have nil compiled regex")
	}
	cat, _ := c.Categorize("something boom happened")
	if cat != "ok" {
		t.Errorf("keyword rule should still match: got %q", cat)
	}
}

func TestEvidencePlaceholderFallback(t *testing.T) {
	got := formatEvidence("{1} failed, error code {9}", []string{"full match", "aclnnAdd", "10001"}, "fallback")
	if got != "{1} failed, error code {9}" {
		t.Errorf("out-of-range placeholder should return template, got %q", got)
	}
	got = formatEvidence("{0} and {2}", []string{"a", "b", "c"}, "")
	if got != "a and c" {
		t.Errorf("got %q, want %q", got, "a and c")
	}
}
