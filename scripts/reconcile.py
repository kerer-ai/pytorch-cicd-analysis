# -*- coding: utf-8 -*-
"""对账引擎（纯分析 + 补丁产出）：不落黑名单副本，变更以补丁形式交人工应用。

数据流：
  1. 运行时读取上游 conf（fetch_upstream.py --fetch-conf 拉取到 --upstream-conf-dir），
     构建 U / 索引 / 日志精确匹配库；本仓库不保存黑名单副本。
  2. 读 parsed_cases.json：加入线含全量 nightly 最近 RECENT_RUNS 次 run 的观测，
     删减线只含本次触发的 blacklist-test run（均带 run_id/ts），不依赖本地历史。
  3. judge_add / judge_remove 判定（judge.py）：加入阈值 ADD_THRESHOLD=3，
     删减以本次 run 为准（REMOVE_THRESHOLD=1）。
  4. 输出 out/candidates.json + out/audit_preview.json + out/summary.md（制品，人工消费）。
  5. 把 add/remove 应用到 conf 内存副本，生成 out/blacklist_update.patch
     （针对上游 ci-test 分支路径的 unified diff，空变更时为空文件）。

补丁由人工在上游仓库根目录 `git apply -p1 <patch>` 应用；本仓库不写黑名单本身。
类型 A/B/C 文件经 serialize/roundtrip 保证最小 diff；a3a5_log_diff 的分组是
「a3 与 a5 日志不一致」的组合，新增无法可靠定位分组，不自动改写，其 remove
候选记入 skipped_removes 交人工。
"""
from __future__ import annotations

import argparse
import datetime
import difflib
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import blacklist_common as bc
import judge

# 补丁内使用的上游相对路径（在上游仓库根目录 git apply -p1 可直接应用）
CONF_REL_PREFIX = ".github/config/blacklist_testccases/conf"


def _serialize_conf(filename: str, data) -> str:
    if filename in bc.TYPE_A_FILES:
        return bc.serialize_type_a(data)
    return json.dumps(data, ensure_ascii=False, indent=2) + "\n"


def _apply_adds(data, filename: str, adds: list):
    """把 add 候选并入 conf 数据；返回 (已应用数, 无法定位 log 分组的 nodeid 列表)。

    conf 的 log 是数组，按其中每个元素建立「元素 -> 分组」映射；
    候选自带 group_log（判定阶段命中的那个元素），据此定位插入分组。
    """
    if filename not in bc.TYPE_A_FILES:
        # 非类型 A 无 log 分组，无法自动定位插入点，交人工
        return 0, [a["nodeid"] for a in adds]
    by_log = {}
    for ent in data:
        for item in bc.normalize_logs(ent.get("log")):
            by_log.setdefault(item.strip(), ent)
    applied, skipped = 0, []
    for a in adds:
        ent = by_log.get((a.get("group_log") or "").strip())
        if ent is None:
            skipped.append(a["nodeid"])
            continue
        nodeids = ent.setdefault("nodeids", {})
        if a["nodeid"] in nodeids:
            continue
        nodeids[a["nodeid"]] = {"cann": a.get("cann", ""), "issue": a.get("issue", "")}
        applied += 1
    return applied, skipped


def _apply_removes(data, filename: str, removes: list) -> int:
    """从 conf 数据删除 remove 候选；分组删空则一并移除。返回删除数。"""
    nids = {r["nodeid"] for r in removes}
    applied = 0
    if filename in bc.TYPE_A_FILES:
        kept = []
        for ent in data:
            nodeids = ent.get("nodeids", {})
            for nid in list(nodeids):
                if nid in nids:
                    del nodeids[nid]
                    applied += 1
            if nodeids:
                kept.append(ent)
        data[:] = kept
    elif filename == bc.FILE_TEMP:
        groups = []
        for g in data.get("groups", []):
            nodes = g.get("nodes", [])
            remain = [n for n in nodes if n not in nids]
            applied += len(nodes) - len(remain)
            g["nodes"] = remain
            if remain:
                groups.append(g)
        data["groups"] = groups
    return applied


def build_blacklist_patch(add, remove, conf_dir, patch_out) -> dict:
    """应用候选到 conf 副本并输出 unified diff（空变更 -> 空文件）。"""
    adds_by, removes_by = {}, {}
    for a in add:
        adds_by.setdefault(a["target_file"], []).append(a)
    for r in remove:
        removes_by.setdefault(r["source_file"], []).append(r)

    diffs, applied_adds, applied_removes, skipped = [], 0, 0, []
    skipped_removes = []
    for filename in sorted(set(adds_by) | set(removes_by)):
        if filename in bc.INDEX_ONLY_FILES:
            # a3a5 的分组是 a3/a5 日志组合，补丁改写易改错分组，交人工
            skipped_removes.extend(r["nodeid"] for r in removes_by.get(filename, []))
            continue
        path = os.path.join(conf_dir, filename)
        with open(path, encoding="utf-8", newline="") as f:
            raw = f.read()
        data = json.loads(raw)
        if filename in adds_by:
            n, sk = _apply_adds(data, filename, adds_by[filename])
            applied_adds += n
            skipped.extend(sk)
        if filename in removes_by:
            applied_removes += _apply_removes(data, filename, removes_by[filename])
        new = _serialize_conf(filename, data)
        if new == raw:
            continue
        diffs.extend(difflib.unified_diff(
            raw.splitlines(keepends=True), new.splitlines(keepends=True),
            fromfile=f"a/{CONF_REL_PREFIX}/{filename}",
            tofile=f"b/{CONF_REL_PREFIX}/{filename}", n=3))

    text = "".join(diffs)
    if text and not text.endswith("\n"):
        text += "\n"
    parent = os.path.dirname(patch_out)
    if parent:
        os.makedirs(parent, exist_ok=True)
    with open(patch_out, "w", encoding="utf-8", newline="") as f:
        f.write(text)
    return {"patch": patch_out, "applied_adds": applied_adds,
            "applied_removes": applied_removes, "skipped_adds": skipped,
            "skipped_removes": skipped_removes, "patch_bytes": len(text)}


def _write_summary_md(add, remove, moved, pending, u_size, out_dir,
                      patch_info=None, join_runs=0, patrol_runs=0):
    lines = [
        "## 黑名单自动对账候选", "",
        f"- 黑名单全集 U（上游 conf 运行时构建）: {u_size}",
        f"- 加入线窗口（上游最近 {bc.RECENT_RUNS} 次全量 run）: {join_runs} 轮，"
        f"阈值连续 failed≥{bc.ADD_THRESHOLD}",
        f"- 删减线（本次触发的 blacklist-test）: {patrol_runs} 轮，"
        f"阈值本次通过（REMOVE_THRESHOLD={bc.REMOVE_THRESHOLD}）",
        f"- add: {len(add)}", f"- remove: {len(remove)}",
        f"- move_passed: {len(moved)}", f"- new_class_pending: {len(pending)}", "",
    ]
    if patch_info is not None:
        lines += [
            f"- 补丁: `{patch_info['patch']}` ({patch_info['patch_bytes']} bytes, "
            f"已应用 add={patch_info['applied_adds']}, remove={patch_info['applied_removes']})",
        ]
        if patch_info["skipped_adds"]:
            lines.append(f"- 未能定位 log 分组、需人工处理: {len(patch_info['skipped_adds'])}")
        if patch_info.get("skipped_removes"):
            lines.append(f"- 位于 a3a5_log_diff、补丁未自动改写、需人工处理: "
                         f"{len(patch_info['skipped_removes'])}")
        lines += ["", "本仓库不落盘黑名单；补丁请在上游仓库根目录 `git apply -p1 <patch>` 应用。", ""]
    else:
        lines += ["", "本仓库不落盘黑名单；候选请人工应用回上游 `Ascend/pytorch` ci-test 分支 conf。", ""]
    if add:
        lines.append("### 建议加入")
        for i in add:
            lines.append(f"- `{i['nodeid']}` -> {i['category']} (`{i['target_file']}`)")
        lines.append("")
    if remove:
        lines.append("### 建议删减")
        for i in remove:
            lines.append(f"- `{i['nodeid']}` ({i['source_file']}) {i['reason']}")
        lines.append("")
    if moved:
        lines.append("### 平台通过迁移")
        for i in moved:
            lines.append(f"- `{i['nodeid']}` -> {i['platform']}")
        lines.append("")
    os.makedirs(out_dir, exist_ok=True)
    with open(os.path.join(out_dir, "summary.md"), "w", encoding="utf-8") as f:
        f.write("\n".join(lines))


def main() -> None:
    p = argparse.ArgumentParser(description="黑名单对账引擎（纯分析）")
    p.add_argument("--parsed", required=True)
    p.add_argument("--upstream-conf-dir", required=True,
                   help="运行时拉取的上游 conf 目录（fetch_upstream.py --fetch-conf）")
    p.add_argument("--out", default="out")
    p.add_argument("--patch-out", default="out/blacklist_update.patch",
                   help="黑名单更新补丁输出路径（空变更时为空文件）")
    p.add_argument("--run-id", type=int, required=True)
    a = p.parse_args()

    ts = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    os.makedirs(a.out, exist_ok=True)

    # 1. 运行时构建 U / 索引 / 精确匹配库（来自上游 conf，非本仓库副本）
    type_a_entries = bc.load_type_a(a.upstream_conf_dir)
    index = bc.build_index(a.upstream_conf_dir)
    u_size = len(bc.collect_nodeids(a.upstream_conf_dir))

    # 2. 判定记录：加入线=最近 N 次全量 run；删减线=本次触发的巡检 run
    payload = json.load(open(a.parsed, encoding="utf-8"))
    parsed = payload["cases"]
    join_runs = len({c.get("run_id", 0) for c in parsed
                     if c.get("source", "full") == "full"})
    patrol_runs = len({c.get("run_id", 0) for c in parsed
                       if c.get("source") == "patrol"})
    records = (judge.build_join_records(parsed, index)
               + judge.build_patrol_records(parsed, index))

    # 3. 判定
    add, pending = judge.judge_add(parsed, index, type_a_entries, records)
    remove, moved = judge.judge_remove(index, records)

    # 4. 候选制品（人工消费；本仓库不落盘黑名单）
    candidates = {"add": add, "remove": remove, "move_passed": moved,
                  "new_class_pending": pending}
    with open(os.path.join(a.out, "candidates.json"), "w", encoding="utf-8") as f:
        json.dump(candidates, f, ensure_ascii=False, indent=2)

    # 4b. 把候选应用到 conf 内存副本 -> 上游格式补丁（不写本仓库黑名单）
    patch_info = build_blacklist_patch(add, remove, a.upstream_conf_dir, a.patch_out)

    audit_preview = {"run_id": a.run_id, "ts": ts, "u_size": u_size,
                     "join_runs": join_runs, "patrol_runs": patrol_runs,
                     "threshold": bc.ADD_THRESHOLD,
                     "remove_threshold": bc.REMOVE_THRESHOLD,
                     "added": len(add), "removed": len(remove),
                     "moved_passed": len(moved), "new_class_pending": len(pending),
                     "window_records": len(records),
                     "patch": patch_info}
    with open(os.path.join(a.out, "audit_preview.json"), "w", encoding="utf-8") as f:
        json.dump(audit_preview, f, ensure_ascii=False, indent=2)
    _write_summary_md(add, remove, moved, pending, u_size, a.out, patch_info,
                      join_runs, patrol_runs)

    print(f"[ANALYZE] U={u_size} join_runs={join_runs} patrol_runs={patrol_runs} "
          f"add={len(add)} "
          f"remove={len(remove)} move_passed={len(moved)} pending={len(pending)} "
          f"records={len(records)} patch={patch_info['patch_bytes']}B "
          f"(applied add={patch_info['applied_adds']}, "
          f"remove={patch_info['applied_removes']})")


if __name__ == "__main__":
    main()
