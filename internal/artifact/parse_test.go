package artifact

import (
	"reflect"
	"testing"
)

func TestParseMDPlannedCounts(t *testing.T) {
	md := `# PyTorch NPU Full Test Summary

## Overview
| Item | Value |
| --- | --- |
| 实际执行用例 | 67128 total |

## 测试文件结果汇总
| 测试文件 | 分片 | 规划用例 | 通过 | 失败 |
| --- | --- | --- | --- | --- |
| test/onnx/test_pytorch_onnx_onnxruntime.py | graph-1, graph-2 | 50,856 | 34744 | 732 |
| test/test\_meta.py | graph-2 | 42,017 | 19387 | 5710 |
| test/test_meta.py | graph-2 | 100 | 19 | 5 |
| test/distributions/test_transforms.py | others-1 | 3282 | 3187 | 26 |
| not-a-test/path.py | x | 999 | 1 | 1 |
| test/bad_count.py | x | N/A | 1 | 1 |
`
	got := ParseMDPlannedCounts([]byte(md))
	want := map[string]int{
		"test/onnx/test_pytorch_onnx_onnxruntime.py": 50856,
		"test/test_meta.py":                     42117, // 转义 `\_` 反转义 + 重复文件累加
		"test/distributions/test_transforms.py": 3282,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseMDPlannedCounts = %v, want %v", got, want)
	}
}

func TestParseShardCasesJSON(t *testing.T) {
	data := []byte(`{"shard": 1, "shard_type": "core", "total_cases": 2,
"cases": [
  {"nodeid": "test/nn/test_convolution.py::TestConv::test_conv", "status": "failed", "duration": 0.1, "message": "RuntimeError: boom", "file": "test/nn/test_convolution.py", "case_idx": 7},
  {"nodeid": "test/nn/test_init.py::TestNNInit::test_orthogonal", "status": "skipped", "message": "test is slow", "file": "test/nn/test_init.py"}
]}`)
	cases, err := ParseShardCasesJSON(data)
	if err != nil {
		t.Fatalf("ParseShardCasesJSON: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("cases = %d, want 2", len(cases))
	}
	if cases[0].NodeID != "test/nn/test_convolution.py::TestConv::test_conv" ||
		cases[0].FilePath != "test/nn/test_convolution.py" ||
		cases[0].Status != "failed" ||
		cases[0].ErrorMessage != "RuntimeError: boom" {
		t.Errorf("case[0] = %+v", cases[0])
	}
	if cases[1].Status != "skipped" || cases[1].ErrorMessage != "test is slow" {
		t.Errorf("case[1] = %+v", cases[1])
	}
}
