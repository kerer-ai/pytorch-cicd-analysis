# -*- coding: utf-8 -*-
"""解析 + 平台标记层（方案 §4.2）：产出 out/parsed_cases.json。

数据源：
  加入线：
    - nightly-main-full-a3.yml 的 test-reports 制品（source=full, platform=a3）
    - nightly-main-full-a5.yml 的 test-reports 制品（source=full, platform=a5）
  删减线：
    - blacklist-test.yml 的 blacklist-report 制品内 blacklist-report.json
      （source=patrol，逐 nodeid 已带 a3/a5 平台字段）

制品结构：
  test-reports-*.zip 内包含 *_cases.json，结构为
  {shard, shard_type, cases:[{nodeid, status, message, file, ...}]}。
  平台上/下游可能是「浅」结构（*_cases.json 直接在外层 zip）或「深」结构
  （外层 zip 内含 test-reports-*.zip，再含 *_cases.json），本解析器两种都兼容。

平台识别：
  full 线依据 workflow 已知平台（显式）；patrol 线直接取 blacklist-report.json
  的 a3_status / a5_status 字段（某平台为 null 表示该平台未执行，不产记录）。

多轮窗口：
  每条线都取「最近 RECENT_RUNS 次」上游 run，各自落在 <root>/<run_id>/ 下，
  run.json 记录 created_at；解析时逐 run 打 run_id/ts，判定阶段即可一次性
  构成连续序列（不再依赖跨轮本地历史）。

输出：out/parsed_cases.json =
  {run_id, runs:[...], cases:[{nodeid, status, log, file, platform, source, run_id, ts}]}
  - 按 (nodeid, platform, run_id) 首现保位去重（避免同平台 shard 内重复计数）。
  - log 取自 message 字段（§3.1 的 log），仅 failed 时保留；patrol 线不需要日志。
"""
from __future__ import annotations

import argparse
import json
import os
import tempfile
import zipfile

import blacklist_common as bc


def detect_platform(*names: str) -> str:
    """从任一文件名文本里识别 a3/a5 字样，优先 a5 再 a3；识别不到返回空串。"""
    for n in names:
        low = (n or "").lower()
        if "a5" in low:
            return "a5"
    for n in names:
        low = (n or "").lower()
        if "a3" in low:
            return "a3"
    return ""


def _parse_cases_json(data: bytes, platform: str, source: str, cases: list) -> None:
    try:
        scf = json.loads(data.decode("utf-8"))
    except (json.JSONDecodeError, UnicodeDecodeError):
        return
    for c in scf.get("cases", []) or []:
        nodeid = (c.get("nodeid") or "").strip()
        if not nodeid:
            continue
        status = c.get("status", "") or ""
        cases.append({
            "nodeid": nodeid,
            "status": status,
            # 只有 failed 需要日志做分类匹配；passed 的日志一律丢弃，控制内存/落盘体积
            "log": (c.get("message", "") or "") if bc.is_failed(status) else "",
            "file": c.get("file", "") or "",
            "platform": platform,
            "source": source,
        })


def _walk(root: str, source: str, platform_hint: str, cases: list) -> None:
    """递归扫描目录树：解析 *_cases.json 与嵌套 zip（test-reports-*.zip 或外层制品）。"""
    for base, _dirs, files in os.walk(root):
        for name in sorted(files):
            full = os.path.join(base, name)
            if name.endswith("_cases.json"):
                platform = platform_hint or detect_platform(name)
                with open(full, "rb") as f:
                    _parse_cases_json(f.read(), platform, source, cases)
            elif name.endswith(".zip"):
                inner_hint = platform_hint or detect_platform(name)
                with tempfile.TemporaryDirectory() as d:
                    try:
                        with zipfile.ZipFile(full) as z:
                            z.extractall(d)
                    except zipfile.BadZipFile:
                        continue
                    _walk(d, source, inner_hint, cases)


def parse_reports_dir(directory: str, source: str, platform: str) -> list:
    """解析一个报告目录（含一层或多层 zip），返回用例列表（未跨平台去重）。"""
    if not directory or not os.path.isdir(directory):
        return []
    cases: list = []
    _walk(directory, source, platform, cases)
    return cases


def _iter_run_dirs(root: str) -> list:
    """遍历 root 下的 run 子目录，返回 [(run_id, ts, path)]，按 ts 升序。"""
    out: list = []
    if not root or not os.path.isdir(root):
        return out
    for name in sorted(os.listdir(root)):
        path = os.path.join(root, name)
        if not os.path.isdir(path):
            continue
        run_id, ts = 0, ""
        meta_path = os.path.join(path, "run.json")
        if os.path.isfile(meta_path):
            try:
                with open(meta_path, encoding="utf-8") as f:
                    meta = json.load(f)
                run_id = int(meta.get("run_id") or 0)
                ts = meta.get("ts") or ""
            except (json.JSONDecodeError, ValueError, TypeError):
                pass
        if not run_id and name.isdigit():
            run_id = int(name)
        if run_id:
            out.append((run_id, ts, path))
    out.sort(key=lambda x: (x[1], x[0]))
    return out


def parse_full_dir(root: str, platform: str) -> list:
    """解析某平台最近 N 次 full 报告目录，逐 run 打 run_id/ts。"""
    cases: list = []
    for run_id, ts, path in _iter_run_dirs(root):
        for c in parse_reports_dir(path, "full", platform):
            c["run_id"] = run_id
            c["ts"] = ts
            cases.append(c)
    return cases


def parse_patrol_dir(root: str) -> list:
    """解析最近 N 次 blacklist-report.json，逐 run 打 run_id/ts。"""
    cases: list = []
    for run_id, ts, path in _iter_run_dirs(root):
        for c in parse_blacklist_report(os.path.join(path, "blacklist-report.json")):
            c["run_id"] = run_id
            c["ts"] = ts
            cases.append(c)
    return cases


def _dedupe(cases: list) -> list:
    """按 (nodeid, platform, run_id) 首现保位去重（同 run 内 shard 重复计数）。"""
    seen = set()
    out = []
    for c in cases:
        key = (c["nodeid"], c["platform"], c.get("run_id", 0))
        if key in seen:
            continue
        seen.add(key)
        out.append(c)
    return out


def parse_blacklist_report(path: str) -> list:
    """解析 blacklist-report.json（扁平数组，一行一个 U 内 nodeid）。

    每行 {"nodeid", "a3_status", "a5_status", "a3_log", "a5_log"}；
    某平台 status 为 null 表示该平台未执行，不产记录——
    缺失即无观测，不能写成 skipped，否则会误打断连续判定。
    """
    if not path or not os.path.isfile(path):
        return []
    with open(path, encoding="utf-8") as f:
        rows = json.load(f)
    cases: list = []
    for row in rows:
        nodeid = (row.get("nodeid") or "").strip()
        if not nodeid:
            continue
        for plat in ("a3", "a5"):
            status = row.get(f"{plat}_status")
            if not status:
                continue
            cases.append({
                "nodeid": nodeid,
                "status": status,
                # 删减判定只看 status；上游每行带 a3_log/a5_log 全量日志，
                # 单次 run 解压后可达数百 MB，这里不保留，避免内存与体积爆炸
                "log": "",
                "file": "",
                "platform": plat,
                "source": "patrol",
            })
    return cases


def main() -> None:
    p = argparse.ArgumentParser(description="解析上游制品（full test-reports + patrol 报告）")
    p.add_argument("--run-id", type=int, required=True, help="本次触发的 run id（仅元信息）")
    p.add_argument("--out", default="out/parsed_cases.json")
    p.add_argument("--full-a3-dir", default="",
                   help="加入线 full-a3 报告根目录（内含 <run_id>/ 子目录）")
    p.add_argument("--full-a5-dir", default="",
                   help="加入线 full-a5 报告根目录（内含 <run_id>/ 子目录）")
    p.add_argument("--patrol-dir", default="",
                   help="删减线根目录（内含 <run_id>/blacklist-report.json）")
    a = p.parse_args()

    all_cases: list = []
    all_cases += parse_full_dir(a.full_a3_dir, "a3")
    all_cases += parse_full_dir(a.full_a5_dir, "a5")
    all_cases += parse_patrol_dir(a.patrol_dir)
    cases = _dedupe(all_cases)

    out_dir = os.path.dirname(a.out)
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)
    runs = sorted({c.get("run_id", 0) for c in cases})
    with open(a.out, "w", encoding="utf-8") as f:
        json.dump({"run_id": a.run_id, "runs": runs, "cases": cases}, f,
                  ensure_ascii=False, indent=2)

    def _count(src, plat=""):
        return sum(1 for c in cases
                   if c["source"] == src and (not plat or c["platform"] == plat))

    failed = [c for c in cases if bc.is_failed(c["status"])]
    print(f"parsed_cases: total={len(cases)} failed={len(failed)} "
          f"runs={len(runs)} "
          f"full_a3={_count('full', 'a3')} full_a5={_count('full', 'a5')} "
          f"patrol={_count('patrol')}")
    print(f"written {a.out}")


if __name__ == "__main__":
    main()