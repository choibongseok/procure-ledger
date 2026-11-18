#!/usr/bin/env python3
"""verify-pg 의 요약 표(B37 10절 7항). `go test -json` 출력을 표준입력으로 받아 패키지별 통과·실패·건너뜀을 표로 보인다.

사용: go test -json ./... | python3 scripts/verify_pg_summary.py [--failures 파일] [--skips 파일]
종료 코드: 실패한 시험이나 빌드 실패가 있으면 1, 없으면 0.
"""
import json
import sys
from collections import defaultdict

failures_path = skips_path = None
args = sys.argv[1:]
while args:
    a = args.pop(0)
    if a == "--detail":
        continue
    if a == "--failures":
        failures_path = args.pop(0)
    elif a == "--skips":
        skips_path = args.pop(0)

pkgs = defaultdict(lambda: {"pass": 0, "fail": 0, "skip": 0, "result": "", "elapsed": 0.0})
failed, skipped, build_failed = [], [], []
output = defaultdict(list)
for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    try:
        e = json.loads(line)
    except ValueError:
        continue
    pkg, test, act = e.get("Package", ""), e.get("Test", ""), e.get("Action", "")
    if test:
        if act == "output":
            output[(pkg, test)].append(e.get("Output", ""))
        if "/" in test and act in ("pass", "fail", "skip"):
            pass  # 하위 시험은 상위와 따로 세지 않는다(상위 결과에 포함)
        elif act == "pass":
            pkgs[pkg]["pass"] += 1
        elif act == "fail":
            pkgs[pkg]["fail"] += 1
            failed.append((pkg, test))
        elif act == "skip":
            pkgs[pkg]["skip"] += 1
            skipped.append((pkg, test, "".join(output[(pkg, test)]).strip().splitlines()[-1:] or [""]))
    else:
        if act in ("pass", "fail", "skip"):
            pkgs[pkg]["result"] = act
            pkgs[pkg]["elapsed"] = e.get("Elapsed", 0.0)
        if act == "fail" and not any(p == pkg for p, _ in failed):
            build_failed.append(pkg)

short = lambda p: p.replace("github.com/choibongseok/procure-ledger/", "")
tp = tf = ts = 0
print()
print("패키지".ljust(44), "결과".ljust(6), "통과".rjust(5), "실패".rjust(5), "건너뜀".rjust(6))
for p in sorted(pkgs):
    d = pkgs[p]
    if not d["result"]:
        continue
    res = {"pass": "통과", "fail": "실패", "skip": "없음"}.get(d["result"], d["result"])
    if d["result"] == "pass" and d["pass"] + d["fail"] + d["skip"] == 0:
        res = "시험 없음"
    print(short(p).ljust(44), res.ljust(6), str(d["pass"]).rjust(5), str(d["fail"]).rjust(5), str(d["skip"]).rjust(6))
    tp, tf, ts = tp + d["pass"], tf + d["fail"], ts + d["skip"]
print("합계".ljust(44), "".ljust(6), str(tp).rjust(5), str(tf).rjust(5), str(ts).rjust(6))
if failed and "--detail" in sys.argv:
    print()
    for p, t in sorted(failed)[:200]:
        msgs = [l.strip() for l in "".join(output[(p, t)]).splitlines() if "_test.go:" in l or "panic" in l]
        print("실패", short(p), t, "::", (msgs[0] if msgs else "")[:240])
if build_failed:
    print("\n시험 실행 전 실패한 패키지(빌드·초기화):", ", ".join(short(p) for p in build_failed))
if failures_path:
    with open(failures_path, "w", encoding="utf-8") as f:
        for p, t in sorted(failed):
            f.write(f"{short(p)}\t{t}\n")
if skips_path:
    with open(skips_path, "w", encoding="utf-8") as f:
        for p, t, why in sorted(skipped):
            f.write(f"{short(p)}\t{t}\t{why[0] if why else ''}\n")
sys.exit(1 if (tf or build_failed) else 0)
