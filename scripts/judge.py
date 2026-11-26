# -*- coding: utf-8 -*-
"""判定器（方案 §6）：加入准入 / 删减准出判定。

- judge_add(parsed, index, type_a_entries, records) -> (add, pending)
  加入准入（§6.1）：failed 且 ∉U 且有 log 且命中已有日志分组，且连续 failed ≥ N。
- judge_remove(index, records) -> (remove, moved)
  删减准出（§6.2）：双平台各自 consecutive_passed ≥ N 且无 failed 回退；
  双平台都成立 -> remove；仅单平台成立 -> move_passed。

N 按 category 分层（§1.4，见 blacklist_common.THRESHOLDS）。
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from typing import List, Tuple

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import blacklist_common as bc

PLATFORMS = ("a3", "a5")


# ---------------------------------------------------------------------------
# 历史记录装配
# ---------------------------------------------------------------------------
def make_record(nodeid, file, category, issue, platform, status, run_id, ts, source=""):
    return {
        "nodeid": nodeid,
        "file": file,
        "category": category,
        "issue": issue,
        "platform": platform,
        "status": status,
        "source": source,  # "full"(加入线) / "patrol"(删减线)，供 status_series 隔离
        "run_id": run_id,
        "ts": ts,
        "consecutive_passed": 0,  # 判定时重算，不信任存量
    }


def build_join_records(parsed_cases, index):
    """加入方向的观测记录（多轮窗口，含「断连」）。

    - source=="full" 且非黑名单的 failed 节点 -> 记 failed。
    - 同一节点在窗口内先 failed 后 passed -> 记 passed 作为「断连哨兵」，
      让 consecutive_failed 在通过后归零（§6.1 连续语义）。
    - 每条记录使用用例自带的 run_id/ts，按时间升序处理。
    """
    cases = [c for c in parsed_cases if c.get("source", "full") == "full"]
    cases.sort(key=lambda c: (c.get("ts", ""), c.get("run_id", 0)))

    prior_failed = set()
    new = []
    seen = set()
    for c in cases:
        nid = c["nodeid"]
        platform = c.get("platform", "")
        run_id = c.get("run_id", 0)
        key = (nid, platform, run_id)
        if key in seen:
            continue
        seen.add(key)
        if nid in index:
            continue  # 已在黑名单，不参与 join
        status = c["status"]
        if bc.is_failed(status):
            new.append(make_record(nid, "", "", "", platform, status, run_id,
                                   c.get("ts", ""), "full"))
            prior_failed.add((nid, platform))
        elif status == "passed" and (nid, platform) in prior_failed:
            new.append(make_record(nid, "", "", "", platform, status, run_id,
                                   c.get("ts", ""), "full"))
    return new


def build_patrol_records(parsed_cases, index):
    """删减方向的观测记录：source=="patrol" 的黑名单节点复测结果。

    blacklist-test 复测的是「已在黑名单」的 nodeid，passed/failed 均入序列，
    供 judge_remove 统计 per-platform 连续 passed；不在黑名单的忽略。
    """
    cases = [c for c in parsed_cases if c.get("source") == "patrol"]
    cases.sort(key=lambda c: (c.get("ts", ""), c.get("run_id", 0)))

    new = []
    seen = set()
    for c in cases:
        nid = c["nodeid"]
        platform = c.get("platform", "")
        run_id = c.get("run_id", 0)
        key = (nid, platform, run_id)
        if key in seen:
            continue
        seen.add(key)
        meta = index.get(nid)
        if meta is None:
            continue  # 不在黑名单，忽略（可能已被删除）
        new.append(make_record(nid, "", meta["category"], meta["issue"],
                               platform, c["status"], run_id, c.get("ts", ""),
                               "patrol"))
    return new


# ---------------------------------------------------------------------------
# 判定
# ---------------------------------------------------------------------------
def judge_add(parsed_cases, index, type_a_entries, records, thresholds=None):
    thresholds = thresholds or bc.THRESHOLDS
    add: list = []
    pending: list = []
    added_nids: set = set()
    pending_nids: set = set()
    for c in parsed_cases:
        if c.get("source", "full") != "full":
            continue
        if not bc.is_failed(c["status"]):
            continue
        nid = c["nodeid"]
        platform = c.get("platform", "")
        if nid in index:                       # 已在黑名单 U
            continue
        log = (c.get("log") or "").strip()
        if not log:                            # 无日志无法匹配
            continue
        res = bc.classify_log(log, type_a_entries)
        if not res.hit:                        # 新 log，交人工确认
            if nid not in pending_nids:
                pending_nids.add(nid)
                pending.append({"nodeid": nid, "log": log,
                                "candidate_category": res.category})
            continue
        cf, _ = bc.consecutive_tail(
            bc.status_series(records, nid, platform, "full"), "failed")
        n = thresholds.get(res.category, 3)
        if cf < n:                             # 连续失败不足 N
            continue
        if nid in added_nids:                  # 黑名单以 nodeid 为键，每 nodeid 只出一次
            continue
        added_nids.add(nid)
        add.append({
            "nodeid": nid,
            "target_file": res.source_file,
            "log": log,
            "group_log": res.group_log,   # 命中的 log 元素，补丁据此定位分组
            "category": res.category,
            "cann": "",
            "issue": bc.ISSUE_BY_CATEGORY.get(res.category, ""),
        })
    return add, pending


def judge_remove(index, records, thresholds=None,
                 enforce_issue_closed: bool = False) -> Tuple[list, list]:
    thresholds = thresholds or bc.THRESHOLDS
    remove: list = []
    moved: list = []
    for nid, meta in index.items():
        if meta["file"] == bc.FILE_PLATFORM:
            # passed_diff 语义的完整删减线待确认（§9.2 / RFC），暂不处理
            continue
        cat = meta["category"]
        n = thresholds.get(cat, 3)
        ok = {}
        for platform in PLATFORMS:
            series = bc.status_series(records, nid, platform, "patrol")
            cp, saw_failed = bc.consecutive_tail(series, "passed")
            ok[platform] = (cp >= n) and (not saw_failed)
        if cat in (bc.CATEGORY_DEVICE, bc.CATEGORY_CANN) and enforce_issue_closed:
            # §6.2：device/cann 类额外校验 issue 已关闭
            # TODO(§9.4)：实现 issue 关闭状态查询（gitcode/gh api），当前 stub 不校验
            pass
        if ok.get("a3") and ok.get("a5"):
            remove.append({"nodeid": nid, "source_file": meta["file"],
                           "reason": "双平台连续通过"})
        elif ok.get("a3"):
            moved.append({"nodeid": nid, "platform": "a3"})
        elif ok.get("a5"):
            moved.append({"nodeid": nid, "platform": "a5"})
    return remove, moved


# ---------------------------------------------------------------------------
# 独立 CLI（便于调试判定逻辑，不写文件，仅打印）
# ---------------------------------------------------------------------------
def main() -> None:
    p = argparse.ArgumentParser(description="判定器：加入/删减候选")
    p.add_argument("--parsed", required=True)
    p.add_argument("--config-dir", default="reports/upstream_conf",
                   help="运行时拉取的上游 conf 目录（fetch_upstream.py --fetch-conf）")
    a = p.parse_args()

    parsed = json.load(open(a.parsed, encoding="utf-8"))["cases"]
    index = bc.build_index(a.config_dir)
    type_a_entries = bc.load_type_a(a.config_dir)
    records = (build_join_records(parsed, index)
               + build_patrol_records(parsed, index))

    add, pending = judge_add(parsed, index, type_a_entries, records)
    remove, moved = judge_remove(index, records)
    print(json.dumps({"add": add, "remove": remove, "move_passed": moved,
                      "new_class_pending": pending},
                     ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()