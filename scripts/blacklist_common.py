# -*- coding: utf-8 -*-
"""黑名单动态管理（自动加入 + 自动删减）共享模块。

提供黑名单三类文件的加载与序列化（类型 A 保持字节级 roundtrip）、全集 U 与
索引构建、日志分组匹配（log 数组整串相等 / 片段包含）与词表粗分、状态序列与
连续计数等基础能力。

本文件被 fetch_upstream / parse_and_classify / judge / reconcile 共同引用，
只依赖标准库，无第三方依赖。

黑名单数据源（重构后）：本仓库不保存黑名单副本；U / 索引 / 日志匹配库
每轮运行时从上游 ci-test conf 拉取（fetch_upstream.py --fetch-conf），
本模块的 *_dir 参数一律指向该运行时目录。serialize_type_a / roundtrip_check_type_a
用于生成与上游格式一致的 patch。
"""
from __future__ import annotations

import json
import os
from dataclasses import dataclass
from typing import Iterable, List, Optional, Tuple

# ---------------------------------------------------------------------------
# category 枚举（§1.4）
# ---------------------------------------------------------------------------
CATEGORY_DTYPE = "dtype not supported"      # 算子未实现某 dtype
CATEGORY_DEVICE = "device not supported"    # DT_DOUBLE/DT_COMPLEX 硬件不支持
CATEGORY_CANN = "cann not supported"        # contiguous / 维度等 CANN 限制
CATEGORY_TEMP = "temporarily skipped"       # 临时跳过

# 连续判定阈值 N：单次运行即拉取上游最近 RECENT_RUNS 次结果构成序列，
# 因此所有类别统一为 RECENT_RUNS（3），否则 N=5 在 3 次窗口内永远无法满足。
RECENT_RUNS = 3
THRESHOLDS = {
    CATEGORY_DTYPE: RECENT_RUNS,
    CATEGORY_DEVICE: RECENT_RUNS,
    CATEGORY_CANN: RECENT_RUNS,
    CATEGORY_TEMP: RECENT_RUNS,
}

# issue 默认映射（对齐 conf/UNSUPPORTED.json 的 category_issues；§9.4 待确认）
ISSUE_BY_CATEGORY = {
    CATEGORY_DEVICE: "https://gitcode.com/Ascend/pytorch/issues/5314",
    CATEGORY_CANN: "https://gitcode.com/Ascend/pytorch/issues/5315",
}

# ---------------------------------------------------------------------------
# 黑名单文件清单（§1.2）
# ---------------------------------------------------------------------------
FILE_DTYPE = "dtype_unsupported_testcases.json"      # 类型 A
FILE_HARDWARE = "hardware_unsupported_testcases.json"  # 类型 A
FILE_A3A5 = "a3a5_log_diff_testcases.json"            # 类型 A 变体（log 为字符串数组）
FILE_TEMP = "temporary_skiped_grouped.json"           # 类型 B
FILE_PLATFORM = "platform_passed_diff.json"           # 类型 C

# 参与日志匹配与补丁改写的类型 A 文件。
TYPE_A_FILES = (FILE_DTYPE, FILE_HARDWARE)
# a3a5_log_diff 每个分组是「a3 与 a5 观测日志不一致」的组合，新增时无法可靠
# 定位分组，因此只参与「U + 索引」（可被 remove 命中，但补丁不自动改写）。
INDEX_ONLY_FILES = (FILE_A3A5,)
ALL_FILES = (FILE_DTYPE, FILE_HARDWARE, FILE_A3A5, FILE_TEMP, FILE_PLATFORM)

# ---------------------------------------------------------------------------
# §5 日志匹配词表（顺序子串匹配，命中即返回；行内任一关键词命中即返回该行 category）
# 仅在日志分组匹配未命中时用作「新类」候选分类（hit=False，不自动建组）。
# 大小写不敏感（对齐仓库 UNSUPPORTED.json 的匹配惯例）。
# ---------------------------------------------------------------------------
WORD_TABLE: List[Tuple[Tuple[str, ...], str]] = [
    (("DT_DOUBLE", "DT_COMPLEX", "float64", "Complex"), CATEGORY_DEVICE),
    (("not implemented for DT_",), CATEGORY_DTYPE),
    (("contiguous", "memory format", "preserve_format"), CATEGORY_CANN),
    (("dtype mismatch", "dtype of", "incorrect dtype"), CATEGORY_DTYPE),
    (("larger than 8 dimensions", "dimension"), CATEGORY_CANN),
    (("scalar_type:", "dtype support list"), CATEGORY_DTYPE),
]

# 失败状态归一化（制品里 Go 侧同时统计 failed / errors 两种）
FAILED_STATUSES = ("failed", "errors", "error")


# ---------------------------------------------------------------------------
# 基础 JSON 读写
# ---------------------------------------------------------------------------
def load_json(path: str):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def normalize_logs(value) -> List[str]:
    """把 conf 的 log 字段归一化为字符串列表（兼容旧的单字符串写法）。

    上游现格式：log 为数组，一个元素代表一条观测到的错误；元素里用 `|`
    连接多个片段时，表示同一错误消息里必须同时出现这些片段。
    """
    if isinstance(value, str):
        return [value] if value else []
    if isinstance(value, list):
        return [x for x in value if isinstance(x, str) and x]
    return []


# ---------------------------------------------------------------------------
# 类型 A 序列化（§1.3 字节级 roundtrip 硬约束）
#
# 输出格式（非标准 json.dump(indent=2)）：
#   [
#     {
#       "log": [
#         "...",
#         "..."
#       ],
#       "category": "...",
#       "nodeids": {
#         "nodeid": {"cann": "", "issue": "..."},
#         ...
#       }
#     },
#     ...
#   ]
# 顶层 `[`；entry `{` 缩进 2；key 缩进 4；log 元素与 nodeid 缩进 6；
# 内层 {cann, issue} 单行（json.dumps 默认分隔符）；空数组写作两行 `[` + `],`；
# 文件末尾 `]\n`。key 顺序沿用原文件（log, category, nodeids）。
# ---------------------------------------------------------------------------
def serialize_type_a(entries: Iterable[dict]) -> str:
    entries = list(entries)
    if not entries:
        return "[]\n"
    lines: List[str] = ["["]
    n = len(entries)
    for i, ent in enumerate(entries):
        lines.append("  {")
        keys = list(ent.keys())
        for k, key in enumerate(keys):
            last = k == len(keys) - 1
            if key == "log":
                logs = normalize_logs(ent.get("log"))
                lines.append('    "log": [')
                for j, item in enumerate(logs):
                    comma = "," if j < len(logs) - 1 else ""
                    lines.append("      " + json.dumps(item, ensure_ascii=False) + comma)
                lines.append("    ]" + ("" if last else ","))
            elif key == "nodeids":
                lines.append('    "nodeids": {')
                nodeids = ent.get("nodeids") or {}
                nids = list(nodeids.keys())
                for j, nid in enumerate(nids):
                    comma = "," if j < len(nids) - 1 else ""
                    meta = json.dumps(nodeids[nid], ensure_ascii=False)  # {"cann": "...", "issue": "..."}
                    lines.append("      " + json.dumps(nid, ensure_ascii=False) + ": " + meta + comma)
                lines.append("    }" + ("" if last else ","))
            else:
                lines.append("    " + json.dumps(key, ensure_ascii=False) + ": "
                             + json.dumps(ent[key], ensure_ascii=False)
                             + ("" if last else ","))
        lines.append("  }" + ("," if i < n - 1 else ""))
    lines.append("]")
    return "\n".join(lines) + "\n"


def load_type_a(config_dir: str) -> List[Tuple[str, List[dict]]]:
    """返回 [(filename, entries), ...]，仅含存在的文件。"""
    out: List[Tuple[str, List[dict]]] = []
    for filename in TYPE_A_FILES:
        path = os.path.join(config_dir, filename)
        if os.path.exists(path):
            out.append((filename, load_json(path)))
    return out


def roundtrip_check_type_a(config_dir: str) -> None:
    """§1.3 强制校验：读入 → 写出 字节级一致，不一致抛 ValueError。"""
    for filename, entries in load_type_a(config_dir):
        path = os.path.join(config_dir, filename)
        with open(path, "r", encoding="utf-8") as f:
            raw = f.read()
        reserialized = serialize_type_a(entries)
        if reserialized != raw:
            raise ValueError(
                "roundtrip mismatch in %s: serialize(parse(x)) != x" % filename
            )


# ---------------------------------------------------------------------------
# 全集 U 与索引（§7 collect_nodeids / 一致性校验）
# ---------------------------------------------------------------------------
def load_extra_entries(config_dir: str) -> List[Tuple[str, List[dict]]]:
    """加载仅参与 U/索引的类型 A 变体文件（a3a5_log_diff_testcases.json）。"""
    out: List[Tuple[str, List[dict]]] = []
    for filename in INDEX_ONLY_FILES:
        path = os.path.join(config_dir, filename)
        if os.path.exists(path):
            out.append((filename, load_json(path)))
    return out


def collect_nodeids(config_dir: str) -> set:
    """生成全集 U：类型 A（含 a3a5 变体）nodeids + 类型 B nodes + 类型 C passed_a3/a5。"""
    nodeids: set = set()
    cfg_dir = config_dir
    for filename in ALL_FILES:
        path = os.path.join(cfg_dir, filename)
        if not os.path.exists(path):
            continue
        data = load_json(path)
        if filename in TYPE_A_FILES or filename in INDEX_ONLY_FILES:
            for ent in data:
                nodeids.update(ent.get("nodeids", {}).keys())
        elif filename == FILE_TEMP:
            for g in data.get("groups", []):
                nodeids.update(g.get("nodes", []))
        elif filename == FILE_PLATFORM:
            nodeids.update(data.get("passed_a3", []))
            nodeids.update(data.get("passed_a5", []))
    return nodeids


def build_index_from_data(type_a_entries: List[Tuple[str, List[dict]]],
                          temp_data: Optional[dict] = None,
                          platform_data: Optional[dict] = None,
                          extra_entries: Optional[List[Tuple[str, List[dict]]]] = None) -> dict:
    """从已加载的 conf 数据构建 nodeid -> {"file", "category", "issue"} 索引。

    数据来自运行时拉取的上游 conf（fetch_upstream.py --fetch-conf），
    本仓库不再维护黑名单静态副本；build_index(dir) 是本函数的按目录加载版。
    extra_entries 为仅参与 U/索引的变体文件，使用 setdefault 以免覆盖主分类信息。
    """
    index: dict = {}
    for filename, entries in type_a_entries:
        for ent in entries:
            cat = ent.get("category", "")
            for nid, meta in ent.get("nodeids", {}).items():
                index[nid] = {
                    "file": filename,
                    "category": cat,
                    "issue": meta.get("issue", "") if isinstance(meta, dict) else "",
                }
    for filename, entries in (extra_entries or []):
        for ent in entries:
            cat = ent.get("category", "")
            for nid, meta in ent.get("nodeids", {}).items():
                index.setdefault(nid, {
                    "file": filename,
                    "category": cat,
                    "issue": meta.get("issue", "") if isinstance(meta, dict) else "",
                })
    if temp_data:
        for g in temp_data.get("groups", []):
            for nid in g.get("nodes", []):
                index[nid] = {
                    "file": FILE_TEMP,
                    "category": g.get("category", ""),
                    "issue": g.get("issue", ""),
                }
    if platform_data:
        for nid in platform_data.get("passed_a3", []):
            index.setdefault(nid, {"file": FILE_PLATFORM, "category": "", "issue": ""})
        for nid in platform_data.get("passed_a5", []):
            index.setdefault(nid, {"file": FILE_PLATFORM, "category": "", "issue": ""})
    return index


def _load_json_if_exists(path: str):
    return load_json(path) if os.path.exists(path) else None


def build_index(config_dir: str) -> dict:
    """按目录加载 conf 后委托 build_index_from_data（config_dir 现指向运行时拉取的上游 conf）。"""
    return build_index_from_data(
        load_type_a(config_dir),
        _load_json_if_exists(os.path.join(config_dir, FILE_TEMP)),
        _load_json_if_exists(os.path.join(config_dir, FILE_PLATFORM)),
        load_extra_entries(config_dir),
    )


# ---------------------------------------------------------------------------
# 日志匹配（§4.2 / §5）三档
# ---------------------------------------------------------------------------
@dataclass
class ClassifyResult:
    category: str
    hit: bool                       # True=命中已有日志分组（可归组），False=新类待人工确认
    source_file: Optional[str] = None   # 命中所在文件名
    group_log: Optional[str] = None     # 命中的 log 元素（补丁据此定位分组）


def _log_fragments(element: str) -> List[str]:
    """log 元素按 `|` 拆片段；全部片段都出现才算同一错误。"""
    return [f.strip() for f in element.split("|") if f.strip()]


def _element_in_message(element: str, message: str) -> bool:
    frags = _log_fragments(element)
    return bool(frags) and all(f in message for f in frags)


def classify_log(log: str, type_a_entries: List[Tuple[str, List[dict]]]) -> ClassifyResult:
    """三档匹配：1) log 元素整串相等；2) 片段全部包含（取最长命中）；
    3) §5 词表规则粗分。

    conf 的 log 是数组，一个元素代表一条观测到的错误；元素用 `|` 连接片段时，
    要求这些片段同时出现在同一条失败日志里。
    """
    log = (log or "").strip()
    if not log:
        return ClassifyResult("", False)
    # 1. 整串相等
    for filename, entries in type_a_entries:
        for ent in entries:
            for item in normalize_logs(ent.get("log")):
                if item.strip() == log:
                    return ClassifyResult(ent.get("category", ""), True,
                                          filename, item.strip())
    # 2. 片段包含：取命中元素最长的一条，避免短片段抢占更具体的分组
    best: Optional[Tuple[int, str, str, str]] = None
    for filename, entries in type_a_entries:
        for ent in entries:
            for item in normalize_logs(ent.get("log")):
                if _element_in_message(item, log):
                    cand = (len(item), filename, ent.get("category", ""), item)
                    if best is None or cand[0] > best[0]:
                        best = cand
    if best is not None:
        return ClassifyResult(best[2], True, best[1], best[3])
    # 2. 规则粗分（候选 category，标记为新类待人工确认）
    low = log.lower()
    for keywords, cat in WORD_TABLE:
        for kw in keywords:
            if kw.lower() in low:
                return ClassifyResult(cat, False)
    return ClassifyResult("", False)


# ---------------------------------------------------------------------------
# 连续计数（记录来自「最近 RECENT_RUNS 次上游 run」窗口，非本地历史）
# 序列分组由 judge._series_map 一次完成，此处只做尾部连续统计。
# ---------------------------------------------------------------------------
def consecutive_tail(series: List[str], target: str, max_n: Optional[int] = None):
    """从序列末尾向前统计连续 == target 的次数，返回 (count, saw_failed)。

    - target 一般传 "passed"（删减）或 "failed"（加入）。
    - 遇到 failed/errors/error 视为 saw_failed=True 并停止；其余（skipped 等）纯中断。
    """
    count = 0
    saw_failed = False
    for st in reversed(series):
        if st == target:
            count += 1
        elif st in FAILED_STATUSES:
            saw_failed = True
            break
        else:
            break
        if max_n is not None and count >= max_n:
            break
    return count, saw_failed


def is_failed(status: str) -> bool:
    return status in FAILED_STATUSES