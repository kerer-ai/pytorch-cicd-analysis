# PyTorch 上游社区 CI/CD 效率分析报告

> **数据来源**: `gh run list -R pytorch/pytorch` 实时查询  
> **数据日期**: 2026-06-24  
> **仓库**: pytorch/pytorch (main branch, commit c004e2d)

---

## 目录

1. [PR 门禁 Workflow](#一pr-门禁-workflow)
2. [Nightly/Periodic 验证 Workflow](#二nightlyperiodic-验证-workflow)
3. [效率关键发现与优化建议](#三效率关键发现与优化建议)
4. [附录: Workflow 分类一览](#附录-workflow-分类一览)

---

## 一、PR 门禁 Workflow

### 1.1 核心必过门禁（PR 合并必须全部通过）

这些 workflow 由 `update-viablestrict.yml`（每 30 分钟运行）检查，全部通过才更新 `viable/strict` 分支。

| # | 功能 | Workflow 名 | 触发条件 | 近三次成功执行时长 | 平均 |
|---|------|------------|----------|---------------------|------|
| 1 | **核心 PR CI** — Linux/Windows/Mac 全平台构建+测试矩阵（多种 CUDA/Python/GCC 组合） | `pull` | `pull_request` + `push` main/release + `schedule` (每日) | 57m, 1h18m, 1h15m | **~1h10m** |
| 2 | **代码 Lint** — clang, pyrefly, clang-tidy, mypy, shellcheck, cmakelint 等 15+ 种 linter | `Lint` | `pull_request` + `push` main/release | 15m, 15m, 5m (PR 触发) | **~12m** |
| 3 | **向后兼容性 Lint** — 检测 API 不兼容变更 | `BC Lint` | `pull_request` | 2m, 2m, 2m | **~2m** |
| 4 | **文档构建** — 构建 Python/C++ 文档预览 | `docs-build` | `pull_request` + `push` main/release | 21m, 23m, 21m | **~22m** |

### 1.2 PR 辅助门禁

| # | 功能 | Workflow 名 | 触发条件 | 近三次成功执行时长 |
|---|------|------------|----------|---------------------|
| 5 | **PR 合并机器人** — `@pytorchbot merge` 触发，验证通过后自动合并 | `Validate and merge PR` | `repository_dispatch` | 4m, 4m, 4m |
| 6 | **ghstack 可合并性检查** — 验证 ghstack PR 的 orig 分支存在 | `Check mergeability of ghstack PR` | `pull_request` (gh/**) | 2m, 2m, 2m |
| 7 | **自动请求 Reviewer** — 根据变更文件自动分配 reviewer | `Auto Request Review` | `pull_request` | 6s, 10s, 9s |

### 1.3 条件触发 PR Workflow（按路径/标签触发，非每次必跑）

| # | 功能 | Workflow 名 | 触发条件 | 近三次成功执行时长 |
|---|------|------------|----------|---------------------|
| 8 | **Inductor 单元测试** — GPU inductor 单元测试 + 内存泄漏检查 | `inductor-unittest` | `pull_request` (path) + `schedule` (每日) | 44m, 44m, 49m |
| 9 | **DTensor 单元测试** — GPU 分布式张量单元测试 (g5.12xlarge) | `dtensor` | `pull_request` (path) + tag | 1h34m, 1h33m, 1h33m |
| 10 | **MAGMA Linux 构建** — CUDA 13.2/13.0/12.9/12.8/12.6 多版本 | `build-linux-magma` | `pull_request` (path) | 45m, 54m, 46m |
| 11 | **MAGMA Windows 构建** — CUDA 矩阵 + Release/Debug | `Build MAGMA for Windows` | `pull_request` (path) | 1h52m, 1h44m, 1h45m |
| 12 | **Triton Wheel 构建** — Linux + Windows Triton 编译器 wheel | `Build Triton wheels` | `pull_request` (path) | 43m, 1h9m, 41m |
| 13 | **Docker 镜像构建** — CI Docker 镜像 (每周三 schedule) | `docker-builds` | `pull_request` (path) + `push` + `schedule` | 1h24m, 42m, 43m |
| 14 | **AlmaLinux Docker 镜像** — CUDA/ROCm/CPU 多配置 | `Build almalinux docker images` | `pull_request` (path) | 33m, 30m, 31m |
| 15 | **Manywheel Docker 镜像** — 二进制 wheel 构建环境 | `Build manywheel docker images` | `pull_request` (path) | 37m, 41m, 38m |
| 16 | **H100 冒烟测试** — sm90 架构有限测试集 | `Limited CI on H100` | `pull_request` (path) + `schedule` (每6h) | 47m, 48m, 44m |
| 17 | **H100 分布式测试** — 8-GPU 分布式 | `Limited CI for distributed tests on H100` | `pull_request` (path) + `schedule` (每日) | 40m, 1h1m, 41m |
| 18 | **H100 CUTLASS 后端测试** — CUTLASS 后端验证 | `Limited CI for CUTLASS backend on H100` | `pull_request` (path) + `schedule` (每12h) | 3h9m, 3h20m, 3h4m |
| 19 | **B200 冒烟测试** — sm100, FP8, FlashAttention | `B200 Smoke Tests` | `pull_request` (path) + `schedule` (每2h) | 2h24m, 2h5m, 1h41m |
| 20 | **CI 工具测试** — lumen_cli 等 | `test-scripts-and-ci-tools` | `pull_request` (path) | (按路径触发, 近30天无记录) |
| 21 | **Quack Vendor 一致性** — 验证 vendored quack 与上游一致 | `Vendored quack reproducibility` | `pull_request` (path) | (按路径触发) |

---

## 二、Nightly/Periodic 验证 Workflow

### 2.1 核心 Nightly/Periodic 构建

| # | 功能 | Workflow 名 | 触发 Schedule | 近三次成功执行时长 | 平均 |
|---|------|------------|---------------|---------------------|------|
| 22 | **Nightly 文档+二进制构建+链接检查** | `nightly` | 每日 UTC 0:00 | 27m, 29m, 15m | **~24m** |
| 23 | **Periodic 全量测试矩阵** — CUDA 13.0, default/distributed/nogpu/jit/multigpu 多配置 | `periodic` | 每 8h (工作日) | 1h25m, 1h44m, 1h34m | **~1h34m** |
| 24 | **Weekly 维护** — 更新 XLA commit hash pin + slow test 列表 | `weekly` | 每周一 7:37 UTC | 4m, 3m, 7m | **~5m** |
| 25 | **更新 viable/strict 分支** — 标记最新全绿 commit | `Update viable/strict` | 每 30 分钟 (:17, :47) | 2m, 2m, 2m | **~2m** |
| 26 | **Unstable 实验性 Jobs** — 每 push main 触发，不阻塞合并 | `unstable` | `push` main + `workflow_dispatch` | 2m, 2m, 6m | **~3m** |
| 27 | **Unstable Periodic** — 实验性 jobs 周期运行 | `unstable-periodic` | 每 4h | 8s, 9s, 12s | **~10s** |

### 2.2 Inductor 性能 Nightly（耗时大户）

| # | 功能 | Workflow 名 | 触发 Schedule | 近三次成功执行时长 | 平均 |
|---|------|------------|---------------|---------------------|------|
| 28 | **A100 Inductor 性能基准** — huggingface/timm/torchbench 全量 benchmark | `inductor-A100-perf-nightly` | 每日 | 7h8m, 7h0m, 9h31m | **~7h53m** |
| 29 | **H100 Inductor 性能基准** — H100 GPU benchmark | `inductor-perf-nightly-h100` | 每日 | 7h28m, 2h18m (PR), 7h40m | **~7h34m** |
| 30 | **B200 Inductor 性能基准** — B200 GPU benchmark | `inductor-perf-b200` | 每日 | 8h24m, 8h7m, 11h0m | **~9h10m** |
| 31 | **ROCm MI300 Inductor 性能** | `inductor-perf-nightly-rocm-mi300` | 每日 | 5h33m, 6h9m, 5h47m | **~5h50m** |
| 32 | **ROCm MI350 Inductor 性能** | `inductor-perf-nightly-rocm-mi350` | 每日 | 5h35m, 6h41m, 8h14m | **~6h50m** |
| 33 | **x86 (Zen) CPU Inductor 性能** | `inductor-perf-nightly-x86-zen` | 每日 | 4h16m, 4h54m, 3h33m | **~4h14m** |
| 34 | **x86 CPU Inductor 性能** | `inductor-perf-nightly-x86` | 每日 | 13m, 13m, 13m | **~13m** |
| 35 | **AArch64 CPU Inductor 性能** | `inductor-perf-nightly-aarch64` | 每日 | 1h21m, 1h18m, 1h21m | **~1h20m** |
| 36 | **macOS (MPS) Inductor 性能** | `inductor-perf-nightly-macos` | 每日 | 4h12m, 4h13m, 4h10m | **~4h12m** |
| 37 | **XPU Inductor 性能** | `inductor-perf-nightly-xpu` | 每日 17:30 UTC | 4h30m, 5h21m, 5h22m | **~5h4m** |
| 38 | **Inductor Periodic** — dynamic/unbacked/AOT 综合测试 | `inductor-periodic` | 每 4h (工作日) | 7h46m, 4h6m, 2h59m | **~4h57m** |
| 39 | **CPU Inductor Nightly** — dynamic_cpu_max_autotune | `inductor-nightly` | 每日 7:00 UTC | 40m, 1h7m, 43m | **~50m** |
| 40 | **Inductor Micro Benchmark (A100)** | `inductor-micro-benchmark` | 每日 7:00 UTC | 1h58m, 48m, 2h4m | **~1h37m** |
| 41 | **Inductor Micro Benchmark (x86)** | `inductor-micro-benchmark-x86` | 每日 7:00 UTC | 27m, 27m, 33m | **~29m** |

### 2.3 Operator 性能 Nightly

| # | 功能 | Workflow 名 | 触发 Schedule | 近三次成功执行时长 | 平均 |
|---|------|------------|---------------|---------------------|------|
| 42 | **Operator Benchmark** — x86 CPU op 性能基准 | `operator_benchmark` | 每周日 7:00 UTC | 16m, 18m, 4h8m (异常值) | **~17m** (正常) |
| 43 | **Operator Micro Benchmark** — H100/A100 GPU op 微基准 | `operator_microbenchmark` | 每日 6:00 UTC | 4h10m, 7h26m, 7h18m | **~6h18m** |
| 44 | **Attention Op Micro Benchmark** — Attention 算子微基准 | `attention_op_microbenchmark` | 每日 7:00 UTC | 5h53m, 8h19m, 7h4m | **~7h5m** |

### 2.4 硬件平台 Nightly

| # | 功能 | Workflow 名 | 触发 Schedule | 近三次成功执行时长 | 平均 |
|---|------|------------|---------------|---------------------|------|
| 45 | **ROCm MI300 CI** — MI300 GPU 构建+测试 | `rocm-mi300` | 每 3h | 2h43m, 4h0m, 4h16m | **~3h40m** |
| 46 | **Periodic ROCm MI300** — MI300 全量测试矩阵 | `periodic-rocm-mi300` | 每 3h | 5h1m, 4h52m, 4h51m | **~4h55m** |
| 47 | **ROCm MI200 CI** — MI200 GPU 构建+测试 | `rocm-mi200` | tag 触发 | 1h16m, 58m, 1h0m | **~1h5m** |
| 48 | **Periodic ROCm MI200** — MI200 全量测试矩阵 | `periodic-rocm-mi200` | 每 3h | 4h16m, 4h57m, 2h29m | **~3h54m** |
| 49 | **ROCm Navi31 CI** — gfx1100 GPU CI | `rocm-navi31` | 每 2h (工作日) | 2h41m, 6h9m, 2h11m | **~3h40m** |
| 50 | **ROCm Nightly 构建** — gfx942 构建 | `rocm-nightly` | 每日 0:00 UTC | 1h54m, 5h35m, 4h26m | **~3h58m** |
| 51 | **Intel XPU CI** — Intel GPU 构建+测试 | `xpu` | 每日 3 次 (工作日) | 2h39m, 2h29m, 2h45m | **~2h38m** |
| 52 | **Mac MPS** — Apple Silicon GPU 构建+测试 | `Mac MPS` | tag 触发 | 47m, 1h28m, 59m | **~1h5m** |
| 53 | **Windows ARM64** — 原生 ARM64 构建+测试 | `windows-arm64-build-test` | 每 4h | 1h3m, 2h11m, 55m | **~1h23m** |

### 2.5 Trunk/Merge 后验证（push 到 main/release 触发）

| # | 功能 | Workflow 名 | 触发条件 | 近三次成功执行时长 | 平均 |
|---|------|------------|----------|---------------------|------|
| 54 | **Trunk 全量 CI** — 比 pull 更全的矩阵 (debug, libtorch, ROCm, AArch64) | `trunk` | `push` main/release + `schedule` (每日) | 3h24m, 3h13m, 3h16m | **~3h17m** |
| 55 | **Slow Tests** — 每次 push main 的慢速全量测试 | `slow` | `push` main/release + `schedule` (每日) | 3h3m, 3h10m, 3h45m | **~3h19m** |
| 56 | **Inductor Trunk** — huggingface/timm/torchbench 集成测试 | `inductor` | `push` main/release | 5h16m, 5h19m, 5h11m | **~5h15m** |
| 57 | **Dynamo Unit Tests** — 多 Python 版本 (3.11/3.12/3.13) + Windows CUDA | `dynamo-unittest` | `push` main/release | 2h56m, 2h55m, 2h52m | **~2h54m** |
| 58 | **TSan** — Thread Sanitizer 构建+测试 | `TSan` | `push` tag + `workflow_dispatch` | 20m, 20m, 19m | **~20m** |
| 59 | **TorchTitan Integration** — TorchTitan 构建+测试 | `torchtitan-test` | `push` main/release | 7m, 5m, 5m | **~6m** |
| 60 | **vLLM Integration** — vLLM x PyTorch 构建+测试 | `vllm-test` | `push` main/release | 3h45m, 3h39m, 3h41m | **~3h42m** |

### 2.6 二进制 Nightly Wheel 构建（push 到 nightly 分支）

| # | 功能 | Workflow 名 | 触发条件 | 近三次成功执行时长 | 平均 |
|---|------|------------|----------|---------------------|------|
| 61 | **Linux x86_64 Wheel** — CUDA/CPU manylinux wheel | `linux-binary-manywheel` | `push` nightly/v* tag | 41m, 6h39m, 6h38m | ~41m (正常) |
| 62 | **Linux AArch64 Wheel** — ARM64 manylinux wheel | `linux-aarch64-binary-manywheel` | `push` nightly/v* tag | 2h44m, 2h44m, 9h44m | ~2h44m (正常) |
| 63 | **macOS ARM64 Wheel** — Apple Silicon wheel | `macos-arm64-binary-wheel` | `push` nightly/v* tag | 1h39m, 2h34m, 1h48m | **~2h0m** |
| 64 | **Windows x86_64 Wheel** — CUDA Windows wheel | `windows-binary-wheel` | `push` nightly/v* tag | 3h20m, 3h13m, 3h19m | **~3h17m** |
| 65 | **Windows ARM64 Wheel** — ARM64 Windows wheel | `windows-arm64-binary-wheel` | `push` nightly/v* tag | 2h13m, 3h25m, 3h21m | **~3h0m** |

---

## 三、效率关键发现与优化建议

### 3.1 耗时 Top 10 Workflow

| 排名 | Workflow | 平均时长 | 类型 | 频率 |
|------|----------|----------|------|------|
| 1 | `inductor-perf-b200` | ~9h10m | Nightly | 每日 |
| 2 | `inductor-A100-perf-nightly` | ~7h53m | Nightly | 每日 |
| 3 | `inductor-perf-nightly-h100` | ~7h34m | Nightly | 每日 |
| 4 | `attention_op_microbenchmark` | ~7h5m | Nightly | 每日 |
| 5 | `inductor-perf-nightly-rocm-mi350` | ~6h50m | Nightly | 每日 |
| 6 | `operator_microbenchmark` | ~6h18m | Nightly | 每日 |
| 7 | `inductor-perf-nightly-rocm-mi300` | ~5h50m | Nightly | 每日 |
| 8 | `inductor` (Trunk) | ~5h15m | Trunk | push main |
| 9 | `inductor-perf-nightly-xpu` | ~5h4m | Nightly | 每日 |
| 10 | `inductor-periodic` | ~4h57m | Nightly | 每 4h (工作日) |

### 3.2 PR 门禁效率分析

| 指标 | 数据 |
|------|------|
| **PR 核心门禁数量** | 4 个（pull, Lint, BC Lint, docs-build） |
| **PR 门禁最短通过时间** | ~2m (BC Lint) |
| **PR 门禁最长通过时间** | ~1h18m (pull) |
| **PR 门禁总耗时（并行）** | 受 `pull` 瓶颈限制，理论最优 ~1h10m |
| **PR 辅助门禁** | 3 个（merge bot, ghstack check, auto reviewer），均 <5m |
| **条件触发 PR workflow** | 14 个，按文件路径选择性触发 |

**关键洞察**:
- `pull` workflow 是 PR 门禁的绝对瓶颈（~1h10m），任何 PR 都必须等待它完成
- `pull` 内部包含 Linux/Windows/Mac 三平台的构建+测试，并行度受可用 runner 数量限制
- Lint（~12m）已经通过条件触发优化（只对变更文件运行对应 linter）
- `docs-build`（~22m）从 `pull` 中分离，允许文档预览更快发布

### 3.3 Nightly/Periodic 资源消耗分析

| 维度 | 数据 |
|------|------|
| **最频繁 Nightly** | `rocm-mi300` / `periodic-rocm-mi300` / `periodic-rocm-mi200`: 每 3h |
| **GPU 资源消耗最大** | Inductor 性能测试 (A100/H100/B200/MI300/MI350/XPU — 每日 7 种 GPU) |
| **每日 GPU 总时长估算** | A100(~8h) + H100(~7.5h) + B200(~9h) + MI300(~6h) + MI350(~7h) + XPU(~5h) + operator(~6h) + attention(~7h) ≈ **55+ GPU小时/天**（仅 Nightly 性能测试） |
| **3h 频率 Periodic** | ROCm MI300/MI200 每 3h 各跑一次，每日 8 次 × ~4-5h = 持续占用 runner |
| **最轻量** | `unstable-periodic` (~10s), `weekly` (~5m), `torchtitan-test` (~6m) |

**关键洞察**:
- ROCm MI300/MI200 的每 3h periodic 频率与单次 ~4-5h 的耗时存在重叠，上一轮未结束下一轮已触发
- Inductor 性能测试覆盖 7 种不同硬件平台，但代码路径高度重叠，存在大量重复计算
- `operator_microbenchmark` (~6h) 和 `attention_op_microbenchmark` (~7h) 耗时很长但每日仅一次

### 3.4 优化建议

| 优先级 | 建议 | 预期收益 |
|--------|------|----------|
| **P0** | **拆分 `pull` workflow** — 将 Linux build+test 按 CUDA 版本/测试类型拆分为独立 job，减少单次门禁的串行等待 | PR 门禁缩短 20-30% |
| **P0** | **ROCm Periodic 频率降低** — `rocm-mi300`/`periodic-rocm-mi300` 从 3h 降到 6h，避免资源重叠浪费 | 释放 ~30% GPU runner 时间 |
| **P1** | **Inductor Perf 去重** — 将 7 种硬件平台的 inductor 基准测试代码路径中的共享部分缓存/跳过 | 减少 15-25% Nightly 耗时 |
| **P1** | **operator_microbenchmark 并行化** — ~6h 的微基准可以按 op 类别拆分并行执行 | 缩短至 1-2h |
| **P2** | **条件触发优化** — 更多 PR workflow 采用 `pull_request.paths` 过滤（已部分实现） | 减少 PR 无关 CI 触发 |
| **P2** | **二进制构建增量化** — Nightly wheel 构建采用 ccache/sccache 和增量编译 | 缩短构建时间 30-50% |

---

## 附录: Workflow 分类一览

### 总览统计

| 类别 | 数量 | 说明 |
|------|------|------|
| **PR 门禁** | 21 | 含 4 个必过 + 3 个辅助 + 14 个条件触发 |
| **Nightly/Periodic** | 31 | 含核心 nightly、inductor 性能、operator 性能、硬件平台 |
| **Trunk/Merge 后** | 7 | push main/release 触发 |
| **二进制 Nightly Wheel** | 5 | push nightly 分支触发 |
| **辅助/工具** | 28 | merge bot, cherry-pick, stale, label, stats upload, AI triage 等 |
| **可复用 workflow** | 25 | `_` 前缀，供其他 workflow 调用 |

### Workflow 触发频率分布

| 频率 | Workflow 示例 |
|------|---------------|
| **每 30 分钟** | `Update viable/strict` |
| **每 2-3 小时** | `rocm-mi300`, `periodic-rocm-mi300`, `periodic-rocm-mi200`, `rocm-navi31` |
| **每 4 小时** | `inductor-periodic`, `unstable-periodic`, `windows-arm64-build-test` |
| **每 8 小时** | `periodic` (工作日) |
| **每日** | `nightly`, `inductor-A100-perf-nightly`, `inductor-perf-nightly-*` (7+ 平台), `operator_microbenchmark`, `attention_op_microbenchmark`, `inductor-nightly`, `xpu` |
| **每周** | `weekly`, `operator_benchmark`, `docker-builds`, `ossf-scorecard` |
| **PR 触发** | `pull`, `Lint`, `BC Lint`, `docs-build` + 14 条件触发 |

---

> **报告生成时间**: 2026-06-24  
> **数据采集方式**: `gh run list -R pytorch/pytorch --workflow="<name>" --status=success --limit=3`  
> **时长计算**: `updatedAt - startedAt`（含队列等待时间）
