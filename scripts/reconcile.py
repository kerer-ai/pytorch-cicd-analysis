# -*- coding: utf-8 -*-
"""对账引擎（纯分析）：候选只出制品，黑名单变更一律人工应用回上游。

数据流（重构后）：
  1. 运行时读取上游 conf（fetch_upstream.py --fetch-conf 拉取到 --upstream-conf-dir），
     构建 U / 索引 / 日志精确匹配库；本仓库不保存黑名单副本。
  2. 读 parsed_cases.json；读 status_history 并追加本轮 join + patrol 记录。
  3. judge_add / judge_remove 判定（judge.py）。
  4. 输出 out/candidates.json + out/audit_preview.json + out/summary.md（制品，人工消费）。
  5. 每轮持久化 status_history/<date>_<run_id>.json（仅本轮新记录；
     跨轮连续判定的唯一状态，与候选多少无关，恒写入）。

不再有 --apply / --commit：对上游黑名单的任何变更由人按候选清单手工执行到
上游 ci-test 分支 conf；serialize/roundtrip 留在 blacklist_common 备用。
"""
from __future__ import annotations

import argparse
import datetime
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import blacklist_common as bc
import judge


def _load_history(history_dir):
    """聚合 status_history/ 下所有轮次文件（每文件只含该轮记录）。"""
    records = []
    if os.path.isdir(history_dir):
        for name in sorted(os.listdir(history_dir)):
            if name.endswith(".json"):
                with open(os.path.join(history_dir, name), encoding="utf-8") as f:
                    records.extend(json.load(f))
    return records


def _write_summary_md(add, remove, moved, pending, u_size, out_dir):
    lines = [
        "## 黑名单自动对账候选", "",
        f"- 黑名单全集 U（上游 conf 运行时构建）: {u_size}",
        f"- add: {len(add)}", f"- remove: {len(remove)}",
        f"- move_passed: {len(moved)}", f"- new_class_pending: {len(pending)}", "",
        "本仓库不落盘黑名单；候选请人工应用回上游 `Ascend/pytorch` ci-test 分支 conf。", "",
    ]
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
    p.add_argument("--history-dir", default="status_history")
    p.add_argument("--out", default="out")
    p.add_argument("--run-id", type=int, required=True)
    a = p.parse_args()

    ts = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    os.makedirs(a.out, exist_ok=True)

    # 1. 运行时构建 U / 索引 / 精确匹配库（来自上游 conf，非本仓库副本）
    type_a_entries = bc.load_type_a(a.upstream_conf_dir)
    index = bc.build_index(a.upstream_conf_dir)
    u_size = len(bc.collect_nodeids(a.upstream_conf_dir))

    # 2. 历史 + 本轮记录
    parsed = json.load(open(a.parsed, encoding="utf-8"))["cases"]
    hist = _load_history(a.history_dir)
    join_new = judge.build_join_records(parsed, index, a.run_id, ts, hist)
    patrol_new = judge.build_patrol_records(parsed, index, a.run_id, ts)
    records = hist + join_new + patrol_new

    # 3. 判定
    add, pending = judge.judge_add(parsed, index, type_a_entries, records)
    remove, moved = judge.judge_remove(index, records)

    # 4. 候选制品（人工消费；本仓库不落盘黑名单）
    candidates = {"add": add, "remove": remove, "move_passed": moved,
                  "new_class_pending": pending}
    with open(os.path.join(a.out, "candidates.json"), "w", encoding="utf-8") as f:
        json.dump(candidates, f, ensure_ascii=False, indent=2)

    # 5. 持久化本轮历史：仅写本轮新记录（写全量会跨轮重复计数），恒写入
    new_records = join_new + patrol_new
    if new_records:
        os.makedirs(a.history_dir, exist_ok=True)
        hist_file = os.path.join(a.history_dir, f"{ts[:10]}_{a.run_id}.json")
        with open(hist_file, "w", encoding="utf-8") as f:
            json.dump(new_records, f, ensure_ascii=False, indent=2)

    audit_preview = {"run_id": a.run_id, "ts": ts, "u_size": u_size,
                     "added": len(add), "removed": len(remove),
                     "moved_passed": len(moved), "new_class_pending": len(pending),
                     "history_records": len(new_records)}
    with open(os.path.join(a.out, "audit_preview.json"), "w", encoding="utf-8") as f:
        json.dump(audit_preview, f, ensure_ascii=False, indent=2)
    _write_summary_md(add, remove, moved, pending, u_size, a.out)

    print(f"[ANALYZE] U={u_size} add={len(add)} remove={len(remove)} "
          f"move_passed={len(moved)} pending={len(pending)} "
          f"history+={len(new_records)}")


if __name__ == "__main__":
    main()
