# -*- coding: utf-8 -*-
"""拉取层：通过 `gh` 处理上游 Ascend/pytorch 的制品，支持四种操作。

对应方案 §4.1（扩展）：
  1. find_run + download_artifact —— 拉「最近一次成功」run 的制品（full-a3 / full-a5 加入线）。
  2. trigger_workflow + wait_completed —— 触发 blacklist-test（ci-test 无参）并等完成（删减线）。
  3. fetch_conf_files —— 拉上游黑名单 conf 原始 JSON（运行时构建 U/索引/匹配库，本仓库不存副本）。

需要环境变量 GH_TOKEN（访问 Ascend/pytorch 须 secrets.ASCEND_PAT，见仓库约束）。

用法：
  # 拉最新成功 run 的 test-reports 制品
  python fetch_upstream.py --workflow nightly-main-full-a3.yml --artifact test-reports --out full-a3.zip
  # 触发（不等待），打印 run_id
  python fetch_upstream.py --trigger-workflow blacklist-test.yml --ref ci-test
  # 等某个 run 完成
  python fetch_upstream.py --wait 123456
  # 拉上游黑名单 conf（构建 U / 日志匹配库的运行时数据源）
  python fetch_upstream.py --fetch-conf --out-dir reports/upstream_conf
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time

DEFAULT_REPO = "Ascend/pytorch"
DEFAULT_WORKFLOW = "nightly-main-full-a3.yml"
DEFAULT_ARTIFACT = "test-reports"
DEFAULT_REF = "ci-test"
# 上游黑名单 conf 所在目录（ci-test 分支）；文件清单以 blacklist_common.ALL_FILES 为准
DEFAULT_CONF_PATH = ".github/config/blacklist_testccases/conf"


def _gh_json(args: list) -> str:
    env = os.environ.copy()
    proc = subprocess.run(["gh", "api", *args], env=env, capture_output=True, text=True)
    if proc.returncode != 0:
        print(proc.stderr, file=sys.stderr)
        raise SystemExit(proc.returncode)
    return proc.stdout


def _gh_bytes(endpoint: str) -> bytes:
    env = os.environ.copy()
    proc = subprocess.run(["gh", "api", endpoint], env=env, capture_output=True)
    if proc.returncode != 0:
        print(proc.stderr.decode("utf-8", "replace"), file=sys.stderr)
        raise SystemExit(proc.returncode)
    return proc.stdout


def _gh_cli(args: list) -> str:
    env = os.environ.copy()
    proc = subprocess.run(["gh", *args], env=env, capture_output=True, text=True)
    if proc.returncode != 0:
        print(proc.stderr, file=sys.stderr)
        raise SystemExit(proc.returncode)
    return proc.stdout


def _gh_raw(endpoint: str) -> bytes:
    """contents API + raw media type：直接返回文件原始字节。"""
    env = os.environ.copy()
    proc = subprocess.run(
        ["gh", "api", "-H", "Accept: application/vnd.github.raw", endpoint],
        env=env, capture_output=True)
    if proc.returncode != 0:
        print(proc.stderr.decode("utf-8", "replace"), file=sys.stderr)
        raise SystemExit(proc.returncode)
    return proc.stdout


def fetch_conf_files(repo: str, ref: str, conf_path: str, out_dir: str,
                     filenames: list) -> None:
    """拉取上游黑名单 conf 原始 JSON 到 out_dir。

    每轮运行时调用，构建 U / 索引 / 日志精确匹配库；本仓库不保存静态副本。
    任一文件缺失即失败（fail-fast）：避免 U 被静默当成空集，误判「不在黑名单」。
    """
    os.makedirs(out_dir, exist_ok=True)
    for name in filenames:
        endpoint = f"repos/{repo}/contents/{conf_path}/{name}?ref={ref}"
        data = _gh_raw(endpoint)
        if not data:
            raise SystemExit(f"empty content for {repo}@{ref}:{conf_path}/{name}")
        path = os.path.join(out_dir, name)
        with open(path, "wb") as f:
            f.write(data)
        print(f"downloaded {repo}@{ref}:{conf_path}/{name} -> {path} "
              f"({len(data)} bytes)")


def find_run(repo: str, workflow: str) -> int:
    """最近一次 completed + success 的 run_id。"""
    jq = (
        '[.workflow_runs[] | select(.status=="completed" and .conclusion=="success")]'
        ' | sort_by(.updated_at) | last | .id'
    )
    out = _gh_json([f"repos/{repo}/actions/workflows/{workflow}/runs?per_page=50",
                    "--jq", jq])
    run_id = out.strip()
    if not run_id:
        raise SystemExit(f"no successful run found for {repo}/{workflow}")
    return int(run_id)


def download_artifact(repo: str, run_id: int, name: str, out_path: str) -> None:
    """下载 run 内名称为 name（startswith）的 artifact zip。"""
    jq = f'.artifacts[] | select(.name | startswith("{name}")) | .id'
    out = _gh_json([f"repos/{repo}/actions/runs/{run_id}/artifacts?per_page=100",
                    "--jq", jq])
    aid = next((line.strip() for line in out.splitlines() if line.strip()), None)
    if not aid:
        raise SystemExit(f"artifact '{name}*' not found for run {run_id}")
    data = _gh_bytes(f"repos/{repo}/actions/artifacts/{aid}/zip")
    with open(out_path, "wb") as f:
        f.write(data)
    print(f"downloaded {name} (artifact_id={aid}, run_id={run_id}) -> {out_path} "
          f"({len(data)} bytes)")


def download_all_artifacts(repo: str, run_id: int, name: str, out_dir: str) -> list:
    """下载 run 内所有 name 前缀匹配的 artifact 到 out_dir，返回保存路径列表。

    一个 run 可能产出多个 test-reports-* 制品（分 shard / 分平台），单取第一个会漏数据，
    故此处全量下载，文件名保留 artifact 名（用于下游平台识别 a3/a5）。
    """
    jq = ('.artifacts[] | select(.name | startswith("{0}")) '
          '| .name + "\\t" + (.id|tostring)').format(name)
    out = _gh_json([f"repos/{repo}/actions/runs/{run_id}/artifacts?per_page=100",
                    "--jq", jq])
    rows = [ln for ln in out.splitlines() if ln.strip()]
    if not rows:
        raise SystemExit(f"artifact '{name}*' not found for run {run_id}")
    os.makedirs(out_dir, exist_ok=True)
    saved = []
    for ln in rows:
        art_name, aid = ln.split("\t", 1)
        data = _gh_bytes(f"repos/{repo}/actions/artifacts/{aid}/zip")
        safe = re.sub(r"[^A-Za-z0-9._-]+", "_", art_name)
        path = os.path.join(out_dir, safe + ".zip")
        with open(path, "wb") as f:
            f.write(data)
        saved.append(path)
        print(f"downloaded {art_name} (artifact_id={aid}, run_id={run_id}) "
              f"-> {path} ({len(data)} bytes)")
    return saved


def trigger_workflow(repo: str, workflow: str, ref: str) -> int:
    """触发 workflow_dispatch（裸触发，无 inputs），返回 run_id（不等待）。"""
    _gh_cli(["workflow", "run", workflow, "--repo", repo, "--ref", ref])
    time.sleep(15)  # 等 run 注册
    out = _gh_cli(["run", "list", f"--workflow={workflow}", "--repo", repo,
                   "--event", "workflow_dispatch", "-L", "1",
                   "--json", "databaseId"])
    runs = json.loads(out or "[]")
    if not runs:
        raise SystemExit(f"could not find triggered run for {workflow}")
    return int(runs[0]["databaseId"])


def wait_completed(repo: str, run_id: int, timeout: int = 7200) -> str:
    """轮询直到 run.status == completed，返回 conclusion（success/failure/...）。

    「结束」不等于「成功」：结束只表示运行完毕，是否成功看 conclusion。
    上游失败时通常没有产出 test-reports 制品，调用方需据此决定是否中止对账
    （不要用不完整数据继续改黑名单）。
    """
    deadline = time.time() + timeout
    while True:
        status = _gh_json([f"repos/{repo}/actions/runs/{run_id}",
                           "--jq", ".status"]).strip()
        if status == "completed":
            conclusion = _gh_json([f"repos/{repo}/actions/runs/{run_id}",
                                   "--jq", ".conclusion"]).strip()
            print(f"run {run_id} completed (conclusion={conclusion})")
            return conclusion
        if time.time() > deadline:
            raise SystemExit(f"timeout waiting for run {run_id} "
                             f"(last status={status})")
        time.sleep(60)


def main() -> None:
    p = argparse.ArgumentParser(description="上游制品拉取 / 触发")
    p.add_argument("--repo", default=os.environ.get("UPSTREAM_REPO", DEFAULT_REPO))
    p.add_argument("--workflow", default=os.environ.get("UPSTREAM_WORKFLOW", DEFAULT_WORKFLOW))
    p.add_argument("--artifact", default=os.environ.get("ARTIFACT_NAME", DEFAULT_ARTIFACT))
    p.add_argument("--out", default="test-reports.zip")
    p.add_argument("--run-id", type=int, default=0, help="指定 run id（跳过自动发现）")
    p.add_argument("--trigger-workflow", default="",
                   help="触发该 workflow（workflow_dispatch）并打印 run_id，不等待")
    p.add_argument("--ref", default=os.environ.get("UPSTREAM_REF", DEFAULT_REF))
    p.add_argument("--wait", type=int, default=0,
                   help="等待该 run 完成并打印 conclusion")
    p.add_argument("--download-all", action="store_true",
                   help="下载所有 name 前缀匹配的 artifact 到 --out-dir")
    p.add_argument("--out-dir", default="reports",
                   help="--download-all / --fetch-conf 时的输出目录")
    p.add_argument("--fetch-conf", action="store_true",
                   help="拉取上游黑名单 conf 原始 JSON 到 --out-dir（运行时数据源）")
    p.add_argument("--conf-path",
                   default=os.environ.get("UPSTREAM_CONF_PATH", DEFAULT_CONF_PATH),
                   help="上游 conf 所在目录路径")
    a = p.parse_args()

    if a.fetch_conf:
        # 文件清单以 blacklist_common.ALL_FILES 为准（U 依赖的 4 个文件），缺一即失败
        sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
        import blacklist_common as bc
        fetch_conf_files(a.repo, a.ref, a.conf_path, a.out_dir, list(bc.ALL_FILES))
        return
    if a.trigger_workflow:
        rid = trigger_workflow(a.repo, a.trigger_workflow, a.ref)
        print(f"run_id={rid}")
        return
    if a.wait:
        conclusion = wait_completed(a.repo, a.wait)
        print(f"run_id={a.wait}")
        print(f"conclusion={conclusion}")
        return

    run_id = a.run_id or find_run(a.repo, a.workflow)
    if a.download_all:
        download_all_artifacts(a.repo, run_id, a.artifact, a.out_dir)
    else:
        download_artifact(a.repo, run_id, a.artifact, a.out)
    print(f"run_id={run_id}")


if __name__ == "__main__":
    main()