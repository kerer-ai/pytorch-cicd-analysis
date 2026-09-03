package report

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func testFS(t *testing.T) fs.FS {
	t.Helper()
	confDir := filepath.Join("..", "..", "conf")
	if _, err := os.Stat(filepath.Join(confDir, "UNSUPPORTED.json")); os.IsNotExist(err) {
		confDir = t.TempDir()
		unsup, _ := os.ReadFile(filepath.Join("..", "..", "conf", "UNSUPPORTED.json"))
		os.WriteFile(filepath.Join(confDir, "UNSUPPORTED.json"), unsup, 0644)
	}
	return os.DirFS(confDir)
}

func testUnsupportedCfg(t *testing.T) *UnsupportedConfig {
	t.Helper()
	cfg, err := LoadUnsupportedConfig(testFS(t), "UNSUPPORTED.json")
	if err != nil {
		t.Fatalf("LoadUnsupportedConfig: %v", err)
	}
	return cfg
}

func TestClassifyUnsupportedPattern(t *testing.T) {
	cfg := testUnsupportedCfg(t)
	cases := []struct {
		msg  string
		want string
	}{
		{"RuntimeError: not implemented for DT_COMPLEX", "not implemented for DT_COMPLEX"},
		{"RuntimeError: not implemented for DT_DOUBLE", "not implemented for DT_DOUBLE"},
		{"Exception: Caused by sample input at index 0 dtype=torch.float64", "Exception: Caused by sample input at index + dtype=torch.float64"},
		{"tensor cannot be larger than 8 dimensions", "cannot be larger than 8 dimensions"},
		{"Jiterator is only supported on CUDA", "Jiterator is only supported on CUDA"},
		{"normal failure message", ""},
	}
	for _, c := range cases {
		got := cfg.checkUnsupportedPattern(c.msg)
		if got != c.want {
			t.Errorf("checkUnsupportedPattern(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
}

// TestClassifyUnsupportedPatternCaseSensitive 大小写敏感（对齐 Python `kw in message`）：
// 小写化的模式文本不应命中。
func TestClassifyUnsupportedPatternCaseSensitive(t *testing.T) {
	cfg := testUnsupportedCfg(t)
	if got := cfg.checkUnsupportedPattern("runtime error: not implemented for dt_complex"); got != "" {
		t.Errorf("lowercased message should not match, got %q", got)
	}
	if got := cfg.checkUnsupportedPattern("jiterator is only supported on cuda"); got != "" {
		t.Errorf("lowercased message should not match, got %q", got)
	}
}

func TestClassifyCase(t *testing.T) {
	ops := []string{"aclnnAdd", "aclnnBatchNorm"}
	cfg := testUnsupportedCfg(t)
	t.Run("passed returns empty", func(t *testing.T) {
		u, r := classifyCase("passed", "any message", ops, cfg)
		if u != "" || r != "" {
			t.Errorf("passed should be empty, got %q %q", u, r)
		}
	})
	t.Run("skipped returns empty", func(t *testing.T) {
		u, r := classifyCase("skipped", "black_skip_flag test", ops, cfg)
		if u != "" || r != "" {
			t.Errorf("skipped should not be unsupported, got %q %q", u, r)
		}
	})
	t.Run("errors status returns empty", func(t *testing.T) {
		u, r := classifyCase("errors", "not implemented for DT_COMPLEX", ops, cfg)
		if u != "" || r != "" {
			t.Errorf("errors status should be ignored (python only failed/error), got %q %q", u, r)
		}
	})
	t.Run("op in message", func(t *testing.T) {
		u, r := classifyCase("failed", "call aclnnAdd failed", ops, cfg)
		if u != "是" || r != "aclnnAdd" {
			t.Errorf("got %q %q", u, r)
		}
	})
	t.Run("pattern takes precedence over op", func(t *testing.T) {
		u, r := classifyCase("failed", "not implemented for DT_COMPLEX while aclnnAdd running", ops, cfg)
		if u != "是" || r != "not implemented for DT_COMPLEX" {
			t.Errorf("pattern should win, got %q %q", u, r)
		}
	})
	t.Run("no match", func(t *testing.T) {
		u, _ := classifyCase("failed", "random error", ops, cfg)
		if u != "" {
			t.Errorf("expected empty, got %q", u)
		}
	})
}

func TestCheckRunningSkiped(t *testing.T) {
	cfg := testUnsupportedCfg(t)
	if !cfg.checkRunningSkiped("Skipped: Only runs on cuda devices") {
		t.Error("cuda-only skip log should match running_skiped")
	}
	if !cfg.checkRunningSkiped("skipped: only runs on CUDA devices") {
		t.Error("running_skiped match should be case-insensitive")
	}
	if cfg.checkRunningSkiped("Skipped: reason A") {
		t.Error("normal skip log should not match")
	}
}

func TestCANNCategory(t *testing.T) {
	cfg := testUnsupportedCfg(t)
	if !cfg.IsCANNCategory("算子未实现") {
		t.Error("算子未实现 should be CANN category")
	}
	if cfg.IsCANNCategory("aclnn调用失败") {
		t.Error("aclnn调用失败 should not be CANN category")
	}
}

func TestExtractFirstLine(t *testing.T) {
	cases := []struct {
		msg, want string
	}{
		{"Skipped: reason A\nline2", "Skipped: reason A"},
		{"\nleading newline", ""},
		{"  spaced first line  \nsecond", "spaced first line"},
		{"single line", "single line"},
		{"", ""},
	}
	for _, c := range cases {
		if got := extractFirstLine(c.msg); got != c.want {
			t.Errorf("extractFirstLine(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
	long := strings200()
	if got := extractFirstLine(long); got != long {
		t.Errorf("extractFirstLine should not truncate, got len %d want len %d", len(got), len(long))
	}
}

func strings200() string {
	b := make([]byte, 500)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
