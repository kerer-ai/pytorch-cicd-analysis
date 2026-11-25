# pytorch-cicd-analysis

Ascend/pytorch CI 分析工具集（Go 报告生成器 + GitHub Actions 工作流）。

## 工作流一览

| 工作流 | 用途 |
| --- | --- |
| `ai-analyze-pipeline-failure` | 输入失败的 Ascend/pytorch 流水线 URL，AI（opencode + GLM-5.3）自动分析用例失败根因 |
| `nightly-decoupling-report` | 拉取 nightly 测试产物，生成分片解耦报告 |
| `trigger-ascend-nightly-*` | 定时触发上游 Ascend/pytorch nightly |
| `monitor-trigger-pr-ci` | 监控上游 PR CI 失败风暴并熔断 |

## AI 流水线失败分析（ai-analyze-pipeline-failure）

输入一条失败的 Ascend/pytorch GitHub Actions 流水线（如
`https://github.com/Ascend/pytorch/actions/runs/45394891072`），在免费
`ubuntu-latest` runner 上用 [opencode](https://opencode.ai)（模型
GLM-5.3，经火山AI网关 OpenAI-compatible 接入）执行本仓库内的
`pipeline-case-failure-analyzer` 技能：自动下载测试报告制品与 CANN 日志、
定位失败用例、判定代码回归或环境偶现、定位引入 PR，输出根因报告。

### 前置：配置 API key secret

仓库 **Settings → Secrets and variables → Actions** 添加 repository secret：

- `OPENCODE_API_KEY`：火山AI网关的 API key（`opencode.jsonc` 通过
  `{env:OPENCODE_API_KEY}` 注入，key 不会出现在仓库代码中）

### 触发方式

```bash
# 单流水线分析
gh workflow run ai-analyze-pipeline-failure \
  -f run_url=https://github.com/Ascend/pytorch/actions/runs/45394891072

# 双流水线回归对比（可选）
gh workflow run ai-analyze-pipeline-failure \
  -f run_url=https://github.com/Ascend/pytorch/actions/runs/<新> \
  -f compare_url=https://github.com/Ascend/pytorch/actions/runs/<旧>
```

或在 GitHub 页面 Actions → AI Analyze Pipeline Failure → Run workflow 手动触发。

### 结果查看

- **Run Summary** 页面直接渲染根因报告；
- Artifact `ai-analysis-report-<run_id>`：`report.md`（根因报告）、
  `summary.json`（结构化摘要）、`analysis-prompt.md`（当次提示词）、
  opencode 运行日志，保留 90 天。

单次分析通常 30–120 分钟（含失败分片制品与 CANN 日志下载、多轮 agent 分析），
job 超时 120 分钟。

## 本地开发

```bash
go build -o reportgen ./cmd/reportgen   # nightly 解耦报告生成器
go test ./...
```
