# -*- coding: utf-8 -*-
"""解析 + 平台标记层（方案 §4.2）：解析 test-reports-*.zip，产出 out/parsed_cases.json。

数据源（三类，均为 test-reports 结构）：
  加入线：
    - nightly-main-full-a3.yml 的 test-reports 制品（source=full, platform=a3）
    - nightly-main-full-a5.yml 的 test-reports 制品（source=full, platform=a5）
  删减线：
    - blacklist-test.yml 的 test-reports 复测产物（source=patrol, platform 自动识别）

制品结构（对齐 Go 侧 loadTestReportsCases / ParseShardCasesJSON）：
  test-reports-*.zip 内包含 *_cases.json，结构为
  {shard, shard_type, cases:[{nodeid, status, message, file, ...}]}。
  平台上/下游可能是「浅」结构（*_cases.json 直接在外层 zip）或「深」结构
  （外层 zip 内含 test-reports-*.zip，再含 *_cases.json），本解析器两种都兼容。

平台识别：
  full 线依据 workflow 已知平台（显式）；patrol 线从文件名里的 a3/a5 字样自动判断
  （依次看 zip 文件名、内层 test-reports-xx 文件名、*_cases.json 文件名），
  识别不到则沿用上层传入的平台 hint（默认为空）。

输出：out/parsed_cases.json = {run_id, cases:[{nodeid, status, log, file, platform, source}]}
  - 按 (nodeid, platform) 首现保位去重（避免同平台 shard 内重复计数）。
  - log 取自 message 字段（§3.1 的 log）。
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
        cases.append({
            "nodeid": nodeid,
            "status": c.get("status", "") or "",
            "log": c.get("message", "") or "",
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


def _dedupe(cases: list) -> list:
    """按 (nodeid, platform) 首现保位去重。"""
    seen = set()
    out = []
    for c in cases:
        key = (c["nodeid"], c["platform"])
        if key in seen:
            continue
        seen.add(key)
        out.append(c)
    return out


def main() -> None:
    p = argparse.ArgumentParser(description="解析 test-reports 制品（三类数据源）")
    p.add_argument("--run-id", type=int, required=True)
    p.add_argument("--out", default="out/parsed_cases.json")
    p.add_argument("--full-a3-dir", default="", help="加入线 full-a3 报告目录")
    p.add_argument("--full-a5-dir", default="", help="加入线 full-a5 报告目录")
    p.add_argument("--patrol-dir", default="", help="删减线 blacklist-test 复测报告目录")
    a = p.parse_args()

    all_cases: list = []
    all_cases += parse_reports_dir(a.full_a3_dir, "full", "a3")
    all_cases += parse_reports_dir(a.full_a5_dir, "full", "a5")
    all_cases += parse_reports_dir(a.patrol_dir, "patrol", "")
    cases = _dedupe(all_cases)

    out_dir = os.path.dirname(a.out)
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)
    with open(a.out, "w", encoding="utf-8") as f:
        json.dump({"run_id": a.run_id, "cases": cases}, f,
                  ensure_ascii=False, indent=2)

    def _count(src, plat=""):
        return sum(1 for c in cases
                   if c["source"] == src and (not plat or c["platform"] == plat))

    failed = [c for c in cases if bc.is_failed(c["status"])]
    print(f"parsed_cases: total={len(cases)} failed={len(failed)} "
          f"full_a3={_count('full', 'a3')} full_a5={_count('full', 'a5')} "
          f"patrol={_count('patrol')}")
    print(f"written {a.out}")


if __name__ == "__main__":
    main()