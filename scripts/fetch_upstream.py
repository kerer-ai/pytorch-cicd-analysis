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
import io
import json
import os
import re
import shutil
import subprocess
import sys
import time
import zipfile

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


def fetch_artifact_member(repo: str, run_id: int, artifact: str, member: str,
                          out_dir: str) -> str:
    """下载名称为 artifact 的制品 zip，取出其中 member 文件写入 out_dir。

    blacklist-report 制品内同时有 xlsx 与 json，只取需要的 json；
    制品不存在（上游该 job 为 continue-on-error）时直接失败，避免用空数据判定。
    """
    jq = f'.artifacts[] | select(.name | startswith("{artifact}")) | .id'
    out = _gh_json([f"repos/{repo}/actions/runs/{run_id}/artifacts?per_page=100",
                    "--jq", jq])
    aid = next((line.strip() for line in out.splitlines() if line.strip()), None)
    if not aid:
        raise SystemExit(f"artifact '{artifact}*' not found for run {run_id}")
    data = _gh_bytes(f"repos/{repo}/actions/artifacts/{aid}/zip")
    os.makedirs(out_dir, exist_ok=True)
    path = os.path.join(out_dir, os.path.basename(member))
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        hit = next((n for n in z.namelist()
                    if n == member or n.endswith("/" + member)), None)
        if hit is None:
            raise SystemExit(f"member '{member}' not found in artifact "
                             f"'{artifact}' (run {run_id}); files={z.namelist()[:20]}")
        # 逐块解压：blacklist-report.json 解压后可达数百 MB，避免整块读入内存
        with z.open(hit) as src, open(path, "wb") as dst:
            shutil.copyfileobj(src, dst, 1024 * 1024)
    size = os.path.getsize(path)
    if size == 0:
        os.remove(path)
        raise SystemExit(f"empty member '{member}' in artifact '{artifact}'")
    print(f"extracted {member} (artifact_id={aid}, run_id={run_id}) -> {path} "
          f"({size} bytes)")
    return path


def recent_runs(repo: str, workflow: str, limit: int, branch: str = "") -> list:
    """最近 limit 次 conclusion=success 的 run，返回 [{run_id, ts}]，按时间升序。"""
    q = f"repos/{repo}/actions/workflows/{workflow}/runs?per_page=50&status=success"
    if branch:
        q += f"&branch={branch}"
    jq = ("[.workflow_runs[] | select(.conclusion==\"success\")]"
          f" | sort_by(.created_at) | reverse | .[:{limit}]"
          " | .[] | \"\\(.id)\\t\\(.created_at)\"")
    out = _gh_json([q, "--jq", jq])
    runs = []
    for line in out.splitlines():
        if "\t" not in line:
            continue
        rid, ts = line.split("\t", 1)
        if not rid.strip().isdigit():
            continue
        runs.append({"run_id": int(rid.strip()), "ts": ts.strip()})
    if not runs:
        raise SystemExit(f"no successful run found for {repo}/{workflow} "
                         f"(branch={branch or '*'})")
    runs.sort(key=lambda r: r["ts"])
    print(f"recent runs of {workflow}: "
          + ", ".join(f"{r['run_id']}@{r['ts']}" for r in runs))
    return runs


def fetch_recent(repo: str, workflow: str, artifact: str, out_dir: str,
                 limit: int, member: str = "", branch: str = "",
                 download_all: bool = False) -> None:
    """下载最近 limit 次「可用」run 的制品，每次落到 out_dir/<run_id>/ 并写 run.json。

    run.json 记录 run_id 与 created_at，供解析层按真实时间排序构成状态序列。
    上游部分 job 是 continue-on-error + always()，会出现 run 成功但制品缺失或
    只传了部分文件（如 blacklist-report 只有 xlsx）；这类 run 直接跳过，
    从更早的成功 run 补足 limit 个，避免用残缺数据判定。
    """
    candidates = recent_runs(repo, workflow, max(limit * 6, 15), branch)
    picked = []
    for r in reversed(candidates):  # 从新到旧
        if len(picked) >= limit:
            break
        d = os.path.join(out_dir, str(r["run_id"]))
        shutil.rmtree(d, ignore_errors=True)
        os.makedirs(d, exist_ok=True)
        try:
            if member:
                fetch_artifact_member(repo, r["run_id"], artifact, member, d)
            elif download_all:
                download_all_artifacts(repo, r["run_id"], artifact, d)
            else:
                download_artifact(repo, r["run_id"], artifact,
                                  os.path.join(d, artifact + ".zip"))
        except SystemExit as exc:
            shutil.rmtree(d, ignore_errors=True)
            print(f"skip run {r['run_id']}: {exc}")
            continue
        with open(os.path.join(d, "run.json"), "w", encoding="utf-8") as f:
            json.dump(r, f, ensure_ascii=False, indent=2)
            f.write("\n")
        picked.append(r)
    if len(picked) < limit:
        raise SystemExit(
            f"only {len(picked)} usable run(s) of {workflow} with artifact "
            f"'{artifact}'{'/' + member if member else ''} (need {limit})")
    picked.sort(key=lambda x: x["ts"])
    print("picked runs: " + ", ".join(f"{r['run_id']}@{r['ts']}" for r in picked))


def fetch_single(repo: str, run_id: int, artifact: str, out_dir: str,
                 member: str = "", download_all: bool = False) -> None:
    """下载「指定 run」的制品到 out_dir/<run_id>/ 并写 run.json。

    与 fetch_recent 不同：这里不做跳过/补足，该 run 就是本轮唯一的判定数据源
    （删减线的 blacklist-test 由本流水线触发），缺制品直接失败。
    """
    ts = _gh_json([f"repos/{repo}/actions/runs/{run_id}",
                   "--jq", ".created_at"]).strip()
    d = os.path.join(out_dir, str(run_id))
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d, exist_ok=True)
    if member:
        fetch_artifact_member(repo, run_id, artifact, member, d)
    elif download_all:
        download_all_artifacts(repo, run_id, artifact, d)
    else:
        download_artifact(repo, run_id, artifact,
                          os.path.join(d, artifact + ".zip"))
    with open(os.path.join(d, "run.json"), "w", encoding="utf-8") as f:
        json.dump({"run_id": run_id, "ts": ts}, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print(f"single run {run_id}@{ts} -> {d}")


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
    p.add_argument("--artifact-member", default="",
                   help="只取 artifact zip 内该文件（如 blacklist-report.json）到 --out-dir")
    p.add_argument("--out-dir", default="reports",
                   help="--download-all / --fetch-conf 时的输出目录")
    p.add_argument("--fetch-conf", action="store_true",
                   help="拉取上游黑名单 conf 原始 JSON 到 --out-dir（运行时数据源）")
    p.add_argument("--recent", type=int, default=0,
                   help="下载最近 N 次成功的 run，每次落 --out-dir/<run_id>/（含 run.json）")
    p.add_argument("--single-run", action="store_true",
                   help="配合 --run-id：只取该 run，落 --out-dir/<run_id>/ 并写 run.json（缺失即失败）")
    p.add_argument("--branch", default="",
                   help="--recent 时按分支过滤（如 main / ci-test）")
    p.add_argument("--conf-path",
                   default=os.environ.get("UPSTREAM_CONF_PATH", DEFAULT_CONF_PATH),
                   help="上游 conf 所在目录路径")
    a = p.parse_args()

    if a.fetch_conf:
        # 文件清单以 blacklist_common.ALL_FILES 为准（U 依赖的 5 个文件），缺一即失败
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

    if a.single_run:
        if not a.run_id:
            raise SystemExit("--single-run 需要同时指定 --run-id")
        fetch_single(a.repo, a.run_id, a.artifact, a.out_dir,
                     a.artifact_member, a.download_all)
        return
    if a.recent:
        fetch_recent(a.repo, a.workflow, a.artifact, a.out_dir, a.recent,
                     a.artifact_member, a.branch, a.download_all)
        return

    run_id = a.run_id or find_run(a.repo, a.workflow)
    if a.artifact_member:
        fetch_artifact_member(a.repo, run_id, a.artifact, a.artifact_member, a.out_dir)
    elif a.download_all:
        download_all_artifacts(a.repo, run_id, a.artifact, a.out_dir)
    else:
        download_artifact(a.repo, run_id, a.artifact, a.out)
    print(f"run_id={run_id}")


if __name__ == "__main__":
    main()