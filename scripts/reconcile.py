# -*- coding: utf-8 -*-
"""对账引擎（方案 §4.4 / §7）：合并 add/remove/move、apply、校验、审计、（可选）提交。

流程：
  1. 读配置（类型 A/B/C）+ 全集 U + 索引。
  2. 读 parsed_cases.json；读并扩充 status_history（join + patrol）。
  3. judge_add / judge_remove（来自 judge.py）。
  4. 输出 out/candidates.json + out/audit_preview.json + out/summary.md。
  5. --apply 时落盘配置与 history/audit，并跑 §7 一致性校验；--commit 时 git 提交推送。

默认 dry-run：不改动 .github/config / status_history / prune_audit，不提交。
"""
from __future__ import annotations

import argparse
import copy
import datetime
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import blacklist_common as bc
import judge


def _load_structures(config_dir):
    """返回 {filename: data} 的可变副本，仅加载存在的文件。"""
    structures = {}
    for filename in bc.ALL_FILES:
        path = os.path.join(config_dir, filename)
        if os.path.exists(path):
            structures[filename] = copy.deepcopy(bc.load_json(path))
    return structures


def _find_entry(entries, log):
    for ent in entries:
        if (ent.get("log") or "").strip() == log.strip():
            return ent
    return None


def apply(structures, add, remove, move_passed):
    """基于候选原地修改 structures；返回审计详情列表。"""
    details = []

    # ---- add：加入类型 A 文件的归组 ----
    for item in add:
        target = item["target_file"]
        if target not in bc.TYPE_A_FILES:
            continue
        entries = structures.setdefault(target, [])
        ent = _find_entry(entries, item["log"])
        if ent is None:
            ent = {"log": item["log"], "category": item["category"], "nodeids": {}}
            entries.append(ent)
        if item["nodeid"] not in ent["nodeids"]:
            ent["nodeids"][item["nodeid"]] = {"cann": item.get("cann", ""),
                                              "issue": item.get("issue", "")}
            details.append({"action": "add", "nodeid": item["nodeid"],
                            "category": item["category"],
                            "issue": item.get("issue", "")})

    # ---- remove：从黑名单组彻底删除 ----
    for item in remove:
        src = item["source_file"]
        if src in bc.TYPE_A_FILES and src in structures:
            _remove_from_type_a(structures[src], item["nodeid"])
            details.append({"action": "remove", "nodeid": item["nodeid"],
                            "reason": item.get("reason", "")})
        elif src == bc.FILE_TEMP and bc.FILE_TEMP in structures:
            for g in structures[bc.FILE_TEMP].get("groups", []):
                if item["nodeid"] in g.get("nodes", []):
                    g["nodes"].remove(item["nodeid"])
            details.append({"action": "remove", "nodeid": item["nodeid"],
                            "reason": item.get("reason", "")})

    # ---- move_passed：从组迁到 platform_passed_diff ----
    for item in move_passed:
        nid = item["nodeid"]
        platform = item["platform"]
        for src in bc.TYPE_A_FILES:
            if src in structures:
                _remove_from_type_a(structures[src], nid)
        if bc.FILE_TEMP in structures:
            for g in structures[bc.FILE_TEMP].get("groups", []):
                if nid in g.get("nodes", []):
                    g["nodes"].remove(nid)
        pd = structures.setdefault(bc.FILE_PLATFORM, {"passed_a3": [], "passed_a5": []})
        other = "passed_a5" if platform == "a3" else "passed_a3"
        if nid in pd.get(other, []):
            pd[other].remove(nid)
        if nid not in pd.setdefault("passed_" + platform, []):
            pd["passed_" + platform].append(nid)
        details.append({"action": "move_passed", "nodeid": nid, "platform": platform})

    return details


def _remove_from_type_a(entries, nodeid):
    for ent in entries:
        if nodeid in ent.get("nodeids", {}):
            del ent["nodeids"][nodeid]
    # 清空空组，避免遗留空 nodeids
    for i in range(len(entries) - 1, -1, -1):
        if not entries[i].get("nodeids"):
            entries.pop(i)


def _write_structures(structures, config_dir):
    os.makedirs(config_dir, exist_ok=True)
    for filename, data in structures.items():
        path = os.path.join(config_dir, filename)
        if filename in bc.TYPE_A_FILES:
            with open(path, "w", encoding="utf-8") as f:
                f.write(bc.serialize_type_a(data))
        else:
            bc.dump_json(data, path)


def validate(structures, old_u, add, remove):
    """§7 一致性校验。返回错误列表（空则通过）。"""
    errors = []

    # 1. 全集守恒（§7.1）
    added = {i["nodeid"] for i in add}
    removed = {i["nodeid"] for i in remove}
    new_u = {nid for nid in (old_u - removed) | added}
    # move_passed 节点仍在 U（迁到 passed_diff），不改变 U 大小
    computed = _collect_from_structures(structures)
    if computed != new_u:
        errors.append(
            "conservation: collect_nodeids mismatch "
            f"(expected {len(new_u)}, got {len(computed)})"
        )

    # 2. passed_a3/a5 互斥且无重复（§7.2）
    pd = structures.get(bc.FILE_PLATFORM, {"passed_a3": [], "passed_a5": []})
    a3 = pd.get("passed_a3", [])
    a5 = pd.get("passed_a5", [])
    if len(a3) != len(set(a3)) or len(a5) != len(set(a5)):
        errors.append("platform_passed_diff: duplicate nodeid within a list")
    if set(a3) & set(a5):
        errors.append("platform_passed_diff: passed_a3/passed_a5 not mutually exclusive")

    # 3. 跨组/跨文件 0 重复（§7.3）
    loc = {}
    dup = []
    for filename in bc.TYPE_A_FILES:
        for ent in structures.get(filename, []):
            for nid in ent.get("nodeids", {}):
                if nid in loc:
                    dup.append(nid)
                loc[nid] = filename
    for g in structures.get(bc.FILE_TEMP, {}).get("groups", []):
        for nid in g.get("nodes", []):
            if nid in loc:
                dup.append(nid)
            loc[nid] = bc.FILE_TEMP
    for platform in ("a3", "a5"):
        for nid in pd.get("passed_" + platform, []):
            if nid in loc:
                dup.append(nid)
            loc[nid] = bc.FILE_PLATFORM
    if dup:
        errors.append(f"duplicate nodeids across files/groups: {dup[:5]}")

    return errors


def _collect_from_structures(structures):
    nodeids = set()
    for filename in bc.TYPE_A_FILES:
        for ent in structures.get(filename, []):
            nodeids.update(ent.get("nodeids", {}).keys())
    for g in structures.get(bc.FILE_TEMP, {}).get("groups", []):
        nodeids.update(g.get("nodes", []))
    pd = structures.get(bc.FILE_PLATFORM, {})
    nodeids.update(pd.get("passed_a3", []))
    nodeids.update(pd.get("passed_a5", []))
    return nodeids


def _write_summary_md(add, remove, moved, pending, errors, out_dir):
    lines = [
        "## 黑名单自动对账候选", "",
        f"- add: {len(add)}", f"- remove: {len(remove)}",
        f"- move_passed: {len(moved)}", f"- new_class_pending: {len(pending)}", "",
    ]
    if errors:
        lines.append("### 一致性校验未通过（不提交）")
        for e in errors:
            lines.append(f"- {e}")
        lines.append("")
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
    p = argparse.ArgumentParser(description="黑名单动态对账引擎")
    p.add_argument("--parsed", required=True)
    p.add_argument("--config-dir", default=".github/config/blacklist_testccases")
    p.add_argument("--history-dir", default="status_history")
    p.add_argument("--audit-dir", default="prune_audit")
    p.add_argument("--out", default="out")
    p.add_argument("--run-id", type=int, required=True)
    p.add_argument("--apply", action="store_true", help="落盘配置 + history + audit")
    p.add_argument("--commit", action="store_true",
                   help="git 提交并推送（隐含 --apply，慎用）")
    a = p.parse_args()

    ts = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    os.makedirs(a.out, exist_ok=True)

    # 1. 读配置
    # 落盘前先做 roundtrip 校验，确保 serializer 与现有文件兼容（§1.3）
    if os.path.isdir(a.config_dir):
        bc.roundtrip_check_type_a(a.config_dir)
    structures = _load_structures(a.config_dir)
    index = bc.build_index(a.config_dir)
    old_u = bc.collect_nodeids(a.config_dir)
    type_a_entries = bc.load_type_a(a.config_dir)

    # 2. 读 parsed + 扩充历史
    parsed = json.load(open(a.parsed, encoding="utf-8"))["cases"]
    records = []
    if os.path.isdir(a.history_dir):
        for name in sorted(os.listdir(a.history_dir)):
            if name.endswith(".json"):
                records.extend(json.load(open(os.path.join(a.history_dir, name),
                                              encoding="utf-8")))
    records += judge.build_join_records(parsed, index, a.run_id, ts, records)
    records += judge.build_patrol_records(parsed, index, a.run_id, ts)

    # 3. 判定
    add, pending = judge.judge_add(parsed, index, type_a_entries, records)
    remove, moved = judge.judge_remove(index, records)
    candidates = {"add": add, "remove": remove, "move_passed": moved,
                  "new_class_pending": pending}

    # 4. 输出候选 + 预览审计
    with open(os.path.join(a.out, "candidates.json"), "w", encoding="utf-8") as f:
        json.dump(candidates, f, ensure_ascii=False, indent=2)
    audit_preview = {"run_id": a.run_id, "ts": ts, "dry_run": not a.apply,
                     "added": len(add), "removed": len(remove),
                     "moved_passed": len(moved), "details": []}

    if a.apply or a.commit:
        details = apply(structures, add, remove, moved)
        errors = validate(structures, old_u, add, remove)
        audit_preview["details"] = details
        if errors:
            print("::error::一致性校验未通过，不落盘：")
            for e in errors:
                print("::error::" + e)
            _write_summary_md(add, remove, moved, pending, errors, a.out)
            sys.exit(1)
        _write_structures(structures, a.config_dir)
        # 写 history（当前轮 join/patrol 记录）+ audit
        os.makedirs(a.history_dir, exist_ok=True)
        hist_file = os.path.join(
            a.history_dir, f"{ts[:10]}_{a.run_id}.json")
        with open(hist_file, "w", encoding="utf-8") as f:
            json.dump(records, f, ensure_ascii=False, indent=2)
        os.makedirs(a.audit_dir, exist_ok=True)
        audit_preview["snapshot_tag"] = f"blacklist-snapshot-{ts[:10]}"
        with open(os.path.join(a.audit_dir, f"{a.run_id}.json"), "w",
                  encoding="utf-8") as f:
            json.dump(audit_preview, f, ensure_ascii=False, indent=2)

        if a.commit:
            _git_commit(a.config_dir, a.history_dir, a.audit_dir, a.run_id)

    # 写摘要/预览（无论是否 apply）
    audit_preview["details"] = audit_preview.get("details", [])
    with open(os.path.join(a.out, "audit_preview.json"), "w", encoding="utf-8") as f:
        json.dump(audit_preview, f, ensure_ascii=False, indent=2)
    _write_summary_md(add, remove, moved, pending,
                      [] if (a.apply or a.commit) else ["dry-run：未落盘，未提交"],
                      a.out)

    mode = "COMMIT" if a.commit else ("APPLY" if a.apply else "DRY-RUN")
    print(f"[{mode}] add={len(add)} remove={len(remove)} "
          f"move_passed={len(moved)} pending={len(pending)}")


def _git_commit(config_dir, history_dir, audit_dir, run_id):
    """git add/commit/push（默认推当前分支 upstream）。"""
    user = os.environ.get("GIT_USER_NAME", "blacklist-bot")
    email = os.environ.get("GIT_USER_EMAIL", "blacklist-bot@users.noreply.github.com")
    subprocess.run(["git", "config", "user.name", user], check=True)
    subprocess.run(["git", "config", "user.email", email], check=True)
    for to_add in (config_dir, history_dir, audit_dir):
        subprocess.run(["git", "add", to_add], check=True)
    msg = f"chore(blacklist): auto reconcile run {run_id}"
    subprocess.run(["git", "commit", "-m", msg], check=True)
    subprocess.run(["git", "push"], check=True)
    print(f"committed and pushed: {msg}")


if __name__ == "__main__":
    main()