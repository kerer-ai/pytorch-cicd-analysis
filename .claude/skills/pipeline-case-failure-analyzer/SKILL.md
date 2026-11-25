---
name: pipeline-case-failure-analyzer
description: "分析 Ascend/pytorch GitHub Actions 流水线中测试用例失败的根因。当用户提供一个失败的流水线 URL，并需要定位哪些用例失败、为什么失败、是哪个 PR 引入的回归时触发。覆盖场景：单流水线失败分析、两个流水线对比回归分析、基于 CANN 日志和测试报告的深度根因定位。触发词：分析流水线失败、用例失败根因、pipeline regression、CI 用例回归、定位引入问题的 PR。"
---

# Pipeline Case Failure Analyzer

分析 Ascend/pytorch CI 流水线中测试用例失败的根因，定位引入回归的 PR 或代码变更。

## 适用场景

- 用户提供一个失败的流水线 URL（`https://github.com/Ascend/pytorch/actions/runs/XXXXX`）
- 用户需要定位失败用例的根因
- 用户需要对比两个流水线找出回归
- 用户需要确认引入问题的具体 PR 或代码变更

## 分析工作流

按以下 6 个阶段依次执行。每个阶段的输出是下一阶段的输入。

### 阶段 1：流水线基本信息采集

获取流水线的 commit、触发方式、job 列表和制品（artifacts）。

```bash
# 获取流水线基本信息（commit、触发时间、状态）
curl -s "https://api.github.com/repos/Ascend/pytorch/actions/runs/<RUN_ID>" | python3 -c "
import json, sys; d = json.load(sys.stdin)
print(f'Commit: {d[\"head_sha\"][:12]}')
print(f'Event: {d[\"event\"]}')
print(f'Status: {d[\"status\"]} / {d[\"conclusion\"]}')
print(f'Created: {d[\"created_at\"]}')
print(f'Workflow: {d[\"name\"]}')
"

# 获取 job 列表和状态
curl -s "https://api.github.com/repos/Ascend/pytorch/actions/runs/<RUN_ID>/jobs" | python3 -c "
import json, sys
for j in json.load(sys.stdin).get('jobs', []):
    print(f'{j[\"name\"]}: {j[\"conclusion\"]} (id={j[\"id\"]})')
"

# 获取 commit 详情（修改了哪些文件）
curl -s "https://api.github.com/repos/Ascend/pytorch/commits/<COMMIT_SHA>" | python3 -c "
import json, sys; d = json.load(sys.stdin)
print(d['commit']['message'].split(chr(10))[0])
for f in d.get('files', [])[:20]:
    print(f'  {f[\"filename\"]} (+{f.get(\"additions\",0)} -{f.get(\"deletions\",0)})')
"
```

记录关键字段：`head_sha`（commit）、`event`（push/pull_request/repository_dispatch）、`name`（workflow 名称）、`created_at`。

### 阶段 2：下载测试报告并定位失败用例

下载 `npu-full-test-summary` 制品获取全局概览，再下载失败分片的 `test-reports-reg-N` 制品获取用例级详情。

```bash
# 下载全局测试摘要
gh run download <RUN_ID> --repo Ascend/pytorch --name npu-full-test-summary --dir <DIR>

# 解析失败用例
python3 -c "
import json
with open('<DIR>/npu-full-test-summary.json') as f:
    data = json.load(f)
print('Totals:', json.dumps(data['totals'], indent=2))
for shard in data.get('shards', []):
    if shard.get('failed', 0) > 0 or shard.get('errors', 0) > 0:
        print(f'Shard {shard[\"shard\"]}: failed={shard[\"failed\"]}, errors={shard[\"errors\"]}')
"

# 下载失败分片的详细报告
gh run download <RUN_ID> --repo Ascend/pytorch --name test-reports-reg-<N> --dir <DIR>

# 提取失败用例的完整信息（nodeid、status、message、command、duration）
python3 -c "
import json
with open('<DIR>/shard_reg-<N>_cases.json') as f:
    data = json.load(f)
for c in data.get('cases', []):
    if c.get('status') in ('failed', 'error'):
        print(f'nodeid: {c[\"nodeid\"]}')
        print(f'status: {c[\"status\"]}')
        print(f'duration: {c.get(\"duration\")}')
        print(f'file: {c.get(\"file\")}')
        print(f'message: {c.get(\"message\", \"\")[:2000]}')
        print()
"
```

同时下载 `cases_logs.tar.gz` 解压，获取每个失败用例的完整 pytest 日志（含 STDOUT/STDERR）。

```bash
mkdir -p <DIR>/cases_logs && cd <DIR>/cases_logs && tar xzf ../cases_logs.tar.gz
# 查看失败用例的详细日志
cat <DIR>/cases_logs/reg-<N>_<IDX>_<test_name>.log
```

### 阶段 3：获取对应 commit 的测试源码

**关键：必须基于失败流水线的 commit 获取测试代码，不能使用本地代码或 main 分支代码。**

```bash
# 获取测试文件源码（基于失败 commit）
curl -s "https://raw.githubusercontent.com/Ascend/pytorch/<COMMIT_SHA>/test/<test_file>.py"

# 如果测试文件在上游 pytorch 仓库（非 Ascend/pytorch）
# 需要确认 Ascend/pytorch 是否直接使用上游测试代码
# 通常 test/nn/test_*.py 等文件随上游 pytorch 源码下载
```

阅读测试代码时重点关注：
- 失败的 assert 语句和期望值 vs 实际值
- 测试的输入构造（特别是边界场景：空输入、零维、非连续等）
- 参考实现（reference implementation）的逻辑
- 测试使用的 dtype、device、shape 参数

### 阶段 4：查找历史流水线并对比

向前查找 24 小时内的流水线，确认失败用例之前是否通过。

```bash
# 列出最近的流水线
curl -s "https://api.github.com/repos/Ascend/pytorch/actions/runs?per_page=20&event=<EVENT_TYPE>" | python3 -c "
import json, sys
for r in json.load(sys.stdin).get('workflow_runs', []):
    print(f'{r[\"id\"]} | {r[\"head_sha\"][:12]} | {r[\"created_at\"]} | {r[\"name\"]} | {r[\"conclusion\"]}')
"
```

找到候选流水线后，下载其测试报告，搜索失败用例是否执行及结果：

```bash
# 下载候选流水线的所有 reg 分片报告
for i in 1 2 3 4 5; do
    gh run download <OLD_RUN_ID> --repo Ascend/pytorch --name test-reports-reg-$i --dir old_reg$i 2>/dev/null &
done; wait

# 在所有分片中搜索目标用例
python3 -c "
import json, glob
targets = ['<失败用例的关键词>']
for f in sorted(glob.glob('old_reg*/shard_reg-*_cases.json')):
    with open(f) as fh:
        data = json.load(fh)
    for c in data.get('cases', []):
        for t in targets:
            if t in c.get('nodeid', ''):
                print(f'{f}: {c[\"nodeid\"]} -> {c[\"status\"]} ({c.get(\"duration\",0):.2f}s)')
"
```

三种可能的对比结果：
1. **之前通过，现在失败**：确认是回归，进入阶段 5 做 PR bisect
2. **之前不存在该用例**：可能是新泛化出的用例，检查 pytorch 上游是否新增了测试参数化
3. **之前也失败**：可能是已知问题，检查 skip_list 是否应该包含该用例

### 阶段 5：分析两个 commit 之间的代码变更

这是最关键的阶段。需要系统性排查两个 commit 之间的所有代码变更。

#### 5.1 获取 commit diff 概览

```bash
# 获取两个 commit 之间的差异概览
curl -s "https://api.github.com/repos/Ascend/pytorch/compare/<OLD_SHA>...<NEW_SHA>" | python3 -c "
import json, sys; d = json.load(sys.stdin)
print(f'Total commits: {d[\"total_commits\"]}')
print(f'Status: {d[\"status\"]}')
print(f'Files changed: {len(d.get(\"files\", []))}')
"
```

#### 5.2 检查子模块变更

Ascend/pytorch 依赖多个子模块，子模块变更往往是问题来源：

```bash
# 检查关键子模块是否变化
for sub in "third_party/op-plugin" "third_party/acl_src/runtime" "third_party/acl_src/ge" "third_party/acl_src/graph-autofusion"; do
    old=$(curl -s "https://api.github.com/repos/Ascend/pytorch/contents/$sub?ref=<OLD_SHA>" | python3 -c "import json,sys; print(json.load(sys.stdin).get('sha','N/A'))")
    new=$(curl -s "https://api.github.com/repos/Ascend/pytorch/contents/$sub?ref=<NEW_SHA>" | python3 -c "import json,sys; print(json.load(sys.stdin).get('sha','N/A'))")
    echo "$sub: old=$old new=$new $([ '$old' = '$new' ] && echo '(unchanged)' || echo '(CHANGED)')"
done
```

如果 op-plugin 变了，获取其 commit 列表：

```bash
curl -s "https://api.github.com/repos/Ascend/op-plugin/compare/<OLD_OP_SHA>...<NEW_OP_SHA>" | python3 -c "
import json, sys; d = json.load(sys.stdin)
for c in d.get('commits', []):
    msg = c['commit']['message'].split(chr(10))[0]
    print(f'{c[\"sha\"][:12]}: {msg}')
"
```

#### 5.3 用本地 git 仓库做更精确的 diff

GitHub API 限制返回 300 个文件。对于大型 diff，使用本地 git 仓库：

```bash
# 在本地 Ascend/pytorch 仓库中
git fetch origin <OLD_SHA> && git fetch origin <NEW_SHA>

# 查看特定目录的逻辑变更（-w 忽略空白变化）
git diff <OLD_SHA>..<NEW_SHA> -w --stat -- torch_npu/csrc/aten/ torch_npu/csrc/framework/

# 查看特定文件的逻辑变更
git diff <OLD_SHA>..<NEW_SHA> -w -- torch_npu/csrc/aten/common/TensorFactories.cpp

# 查看特定 commit
git show <COMMIT_SHA> -- torch_npu/
```

#### 5.4 系统性排查清单

按以下顺序排查代码变更：

| 排查项 | 方法 | 优先级 |
|---|---|---|
| 失败用例直接调用的算子代码 | 对比 op_plugin 中对应算子的 opapi 和 aclops 文件 | P0 |
| 算子执行框架代码 | 对比 op_api_common.h 中 EXEC_NPU_CMD 宏的变更 | P0 |
| 内存分配器代码 | 对比 NPUCachingAllocator.cpp 的逻辑变更 | P1 |
| deterministic/caching 机制 | 对比 OpParamMaker.cpp 中 SetDeterministic 相关变更 | P1 |
| Copy/Cast kernel | 对比 CopyKernel.cpp 和 CopyKernelOpApi.cpp 的变更 | P1 |
| Tensor factory 代码 | 对比 TensorFactories.cpp 中 empty/empty_strided 的变更 | P2 |
| Format cast 代码 | 对比 FormatCastKernelNpu.cpp 的变更 | P2 |
| 测试文件本身 | 确认测试代码是否变更 | P2 |

### 阶段 6：下载并分析 CANN 日志

CANN 日志提供算子层面的执行信息，是根因定位的关键证据。

```bash
# 下载 CANN 日志制品
gh run download <RUN_ID> --repo Ascend/pytorch --name ascend-cann-logs-reg-<N> --dir cann_logs
# 或 dist 分片
gh run download <RUN_ID> --repo Ascend/pytorch --name ascend-cann-logs-dist-<N> --dir cann_logs

# 解压
mkdir -p cann_logs_ext && cd cann_logs_ext && tar xzf ../ascend-cann-logs-reg-<N>.tar.gz
```

CANN 日志结构：
- `ascend/log/debug/plog/` — 算子执行日志（包含 ERROR 和 INFO）
- `ascend/log/run/device-N/` — 设备级运行日志

```bash
# 搜索失败算子的 CANN 日志
grep -ri "<算子名>" cann_logs_ext/ascend/log/debug/plog/ | head -20

# 过滤 ERROR 日志
grep -r "\[ERROR\]" cann_logs_ext/ascend/log/debug/plog/ | grep -i "<算子名>" | head -20

# 检查是否有 dtype 不支持的错误
grep -ri "CheckDtypeValid\|not implemented for" cann_logs_ext/ascend/log/debug/plog/ | head -10
```

CANN 日志分析的典型模式：
- `CheckDtypeValid` 失败 → dtype 不支持，预期行为（会 fallback 到 aclop）
- `GetWorkspaceSize` 失败 → 算子参数检查未通过
- 无 ERROR 日志但结果错误 → 算子执行了但输出不正确（内存未初始化等）

## 根因分析框架

当定位到失败用例后，按以下框架分析根因：

### 模式 1：数值精度问题

错误特征：`Tensor-likes are not close!`、`Mismatched elements`

分析路径：
1. 检查输出张量的内存分配方式（是否初始化）
2. 检查 opapi 路径是否 fallback 到 aclop（DO_COMPATIBILITY 宏）
3. 对比 opapi 和 aclop 路径对边界场景（空输入、零维等）的处理差异
4. 检查 CANN 算子是否正确写入全部输出元素

### 模式 2：算子执行错误

错误特征：`RuntimeError`、`aclnn xxx failed`

分析路径：
1. 检查 CANN plog 中的 ERROR 日志
2. 确认算子是否支持当前 dtype/格式
3. 检查 DO_COMPATIBILITY 是否正确 fallback

### 模式 3：Crash/超时

错误特征：`SIGSEGV`、`timeout`、`Segmentation fault`

分析路径：
1. 检查 NPU 设备中毒（_check_npu_poisoned）
2. 检查内存越界
3. 检查多进程/多线程竞争

### 模式 4：回归分析（两个流水线对比）

当确认是回归时，代码变更的三种典型模式：

1. **直接代码变更**：失败算子的代码文件有修改 → 直接分析 diff
2. **框架层变更**：op_api_common.h、OpParamMaker.cpp 等执行框架变更 → 分析对算子执行路径的影响
3. **内存分配变更**：NPUCachingAllocator 变更 → 分析对输出张量内存初始化状态的影响

关键判断：代码变更是否改变了以下行为之一：
- 输出张量的内存分配和初始化方式
- PTA cache 的命中/未命中行为
- deterministic 设置的时序和频率
- 算子的 fallback 路径选择

## 产出格式

分析完成后，输出以下结构化报告：

```
## 流水线分析报告

### 1. 流水线对比
| 维度 | 旧流水线 | 新流水线 |
|---|---|---|
| Commit | ... | ... |
| 测试总数 | ... | ... |
| 失败数 | ... | ... |

### 2. 失败用例
（列表，含 nodeid、status、duration、error message）

### 3. 根因分析
（数值精度/算子错误/crash/回归的具体分析）

### 4. 引入问题的 PR
（具体 commit SHA 和描述，以及因果链）

### 5. 修复建议
（方案 A/B/C，含代码示例）
```

## 注意事项

1. **必须基于对应 commit 获取代码**：不能使用本地代码或 main 分支代码，因为代码可能已变化
2. **op_plugin 是子模块**：op_plugin 的 commit 独立于 Ascend/pytorch，需要单独检查
3. **CANN 子模块可能不变**：两个 commit 之间 CANN runtime/ge 子模块可能完全相同，问题在 torch_npu 代码层
4. **Docker 镜像标识相同不代表内容相同**：但通常 CI 使用固定镜像，可信任标识
5. **nightly vs PR CI 的测试集不同**：nightly 不做 skip_list 过滤，PR CI 会过滤已知失败，导致测试集和内存状态不同
6. **PTA cache 是进程内缓存**：在同一测试 session 中跨用例共享，哈希键变化可能影响后续所有算子
7. **CANN plog 可能不完整**：制品大小有限，plog 可能只覆盖部分时间窗口
