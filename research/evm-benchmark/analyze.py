"""Joins step-*.json (per-call rows), env.csv (10 s environment samples) and the
chain's own momentum record into summary.json for the report page."""
import csv, glob, json, os, statistics, urllib.request

D = os.path.dirname(os.path.abspath(__file__))
RPC = "http://localhost:35997"


def rpc(method, params):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode()
    req = urllib.request.Request(RPC, body, {"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=60))["result"]


def pct(xs, p):
    if not xs:
        return None
    xs = sorted(xs)
    k = (len(xs) - 1) * p / 100
    lo, hi = int(k), min(int(k) + 1, len(xs) - 1)
    return round(xs[lo] + (xs[hi] - xs[lo]) * (k - lo), 1)


def dist(xs):
    return {"n": len(xs), "p50": pct(xs, 50), "p90": pct(xs, 90), "p99": pct(xs, 99),
            "max": max(xs) if xs else None, "mean": round(statistics.mean(xs), 1) if xs else None}


def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


env = []
for path in glob.glob(os.path.join(D, "env*.csv")):
    with open(path) as f:
        for r in csv.DictReader(f):
            env.append({k: (num(v) if k not in ("rpc_cpu", "rpc_mem") else v) for k, v in r.items()})
env.sort(key=lambda e: e["ts"])


def mem_mib(s):
    if not s:
        return None
    s = s.strip()
    for unit, mul in (("GiB", 1024), ("MiB", 1), ("KiB", 1 / 1024)):
        if s.endswith(unit):
            return float(s[: -len(unit)]) * mul
    return None


for e in env:
    e["rpc_cpu"] = num((e.get("rpc_cpu") or "").rstrip("%"))
    e["rpc_mem_mib"] = mem_mib(e.get("rpc_mem"))
    e["sync_lag"] = (e["node_height"] - e["sync_height"]) if e.get("node_height") and e.get("sync_height") else None
    e["momentum_age"] = (e["ts"] / 1000 - e["momentum_ts"]) if e.get("momentum_ts") else None


def env_window(t0, t1):
    return [e for e in env if t0 <= e["ts"] <= t1]


def height_at(ts_ms):
    best = None
    for e in env:
        if e.get("momentum") and e["ts"] <= ts_ms:
            best = int(e["momentum"])
    return best


# Every momentum across the whole benchmark, once.
h_lo = int(min(e["momentum"] for e in env if e.get("momentum")))
h_hi = int(max(e["momentum"] for e in env if e.get("momentum")))
moms = {}
h = h_lo
while h <= h_hi:
    for m in rpc("ledger.getMomentumsByHeight", [h, 1000])["list"]:
        moms[m["height"]] = m
    h += 1000


def chain_window(t0_ms, t1_ms):
    ms = sorted((m for m in moms.values() if t0_ms / 1000 <= m["timestamp"] <= t1_ms / 1000),
                key=lambda m: m["height"])
    gaps = [b["timestamp"] - a["timestamp"] for a, b in zip(ms, ms[1:])]
    blocks = [len(m["content"]) for m in ms]
    return {
        "momentums": len(ms),
        "interval": dist(gaps),
        "stalls_over_30s": sum(1 for g in gaps if g > 30),
        "blocks_per_momentum": dist(blocks),
        "blocks_total": sum(blocks),
        "fusion_price_max": max((m.get("nextFusionPrice") or 0 for m in ms), default=None),
        "series": [{"h": m["height"], "t": m["timestamp"], "b": len(m["content"]),
                    "p": m.get("nextFusionPrice")} for m in ms],
    }


def delta(win, key):
    vals = [e[key] for e in win if e.get(key) is not None]
    return (vals[-1] - vals[0]) if len(vals) >= 2 else None


steps = []
for path in sorted(glob.glob(os.path.join(D, "step-*.json")), key=lambda p: int(p.split("-")[-1][:-5])):
    rep = json.load(open(path))
    rows = rep["rows"] or []
    t0, t1 = rep["loadStartMs"], rep["loadEndMs"]
    load_s = (t1 - t0) / 1000
    ok = [r for r in rows if r["stage"] == "ok"]
    bad = [r for r in ok if r["op"] == "counter.bad-selector"]
    expected_revert_ok = [r for r in bad if not r["success"]]
    unexpected = [r for r in rows if r["stage"] != "ok"] + \
                 [r for r in ok if r["op"] != "counter.bad-selector" and not r["success"]] + \
                 [r for r in bad if r["success"]]
    errs = {}
    for r in unexpected:
        key = f'{r["stage"]}: {(r.get("err") or "reverted")[:120]}'
        errs[key] = errs.get(key, 0) + 1
    fees = [int(r["feeRaw"]) for r in rows if r.get("feeRaw")]
    by_op = {}
    for r in ok:
        by_op.setdefault(r["op"], []).append(r["gasUsed"])
    fund_s = (rep["loadStartMs"] - rep["fundStartMs"]) / 1000
    win = env_window(rep["fundStartMs"], t1 + 15000)
    lwin = env_window(t0, t1 + 15000)
    steps.append({
        "wallets": rep["wallets"], "run": rep["run"], "load_s": round(load_s, 1), "fund_s": round(fund_s, 1),
        "started": len(rows), "completed": len(ok),
        "throughput_per_min": round(len(ok) / load_s * 60, 1) if load_s else None,
        "expected_reverts": len(expected_revert_ok),
        "unexpected": len(unexpected), "unexpected_by_cause": errs,
        "mismatches": rep["mismatches"],
        "latency_ms": {k: dist([r[k] for r in ok]) for k in ("publishMs", "confirmMs", "indexMs", "totalMs")},
        "fee_raw": dist(fees), "fee_total_raw": sum(fees),
        "base_plasma": dist([r["basePlasma"] for r in ok if r["basePlasma"]]),
        "fused_plasma": dist([r["fusedPlasma"] for r in ok if r["fusedPlasma"]]),
        "pow_blocks": sum(1 for r in ok if r["difficulty"]),
        "data_bytes": dist([r["dataBytes"] for r in rows]),
        "gas_by_op": {k: dist(v) for k, v in by_op.items()},
        "chain": chain_window(t0, t1),
        "env": {
            "disk_rpc_delta": delta(win, "disk_rpc"),
            "disk_pillar_delta": delta(win, "disk_pillar"),
            "db_delta": delta(win, "db_size_api"),
            "evm_txs_delta": delta(win, "evm_txs"),
            "events_delta": delta(win, "events"),
            "evm_rebuild_ms": dist([e["evm_rebuild_ms"] for e in lwin if e.get("evm_rebuild_ms") is not None]),
            "overview_ms": dist([e["overview_ms"] for e in lwin if e.get("overview_ms") is not None]),
            "info_ms": dist([e["info_ms"] for e in lwin if e.get("info_ms") is not None]),
            "sync_lag": dist([e["sync_lag"] for e in lwin if e.get("sync_lag") is not None]),
            "momentum_age_s": dist([e["momentum_age"] for e in lwin if e.get("momentum_age") is not None]),
            "serve_rss_max": max((e["serve_rss"] for e in lwin if e.get("serve_rss")), default=None),
            "rpc_cpu": dist([e["rpc_cpu"] for e in lwin if e.get("rpc_cpu") is not None]),
            "rpc_mem_mib_max": max((e["rpc_mem_mib"] for e in lwin if e.get("rpc_mem_mib")), default=None),
        },
        "rows": [{"w": r["wallet"], "op": r["op"], "t": r["startMs"], "p": r["publishMs"], "c": r["confirmMs"],
                  "i": r["indexMs"], "s": r["stage"], "ok": r["success"]} for r in rows],
    })

first, last = env[0], env[-1]
summary = {
    "window": {"start": first["ts"], "end": last["ts"], "h_lo": h_lo, "h_hi": h_hi},
    "baseline": chain_window(first["ts"], steps[0]["rows"][0]["t"] if steps and steps[0]["rows"] else first["ts"] + 60000),
    "totals": {k: (last.get(k) - first.get(k)) if last.get(k) is not None and first.get(k) is not None else None
               for k in ("disk_rpc", "disk_pillar", "disk_pillar2", "disk_pillar3", "disk_observer",
                         "db_size_api", "db_bytes", "wal_bytes", "content_bytes", "evm_content_bytes",
                         "evm_txs", "events", "serve_cpu_s")},
    "end_state": {k: last.get(k) for k in ("disk_rpc", "db_size_api", "evm_txs", "evm_content_bytes",
                                            "evm_rebuild_ms", "serve_rss", "events", "content_bytes")},
    "start_state": {k: first.get(k) for k in ("disk_rpc", "db_size_api", "evm_txs", "evm_content_bytes",
                                                "evm_rebuild_ms", "serve_rss", "events", "content_bytes")},
    "env_series": [{k: e.get(k) for k in ("ts", "disk_rpc", "db_size_api", "serve_rss", "evm_txs", "evm_rebuild_ms",
                                          "overview_ms", "sync_lag", "momentum_age", "rpc_cpu", "fusion_price")}
                   for e in env],
    "steps": steps,
}
json.dump(summary, open(os.path.join(D, "summary.json"), "w"))
for s in steps:
    L = s["latency_ms"]["totalMs"]
    print(f'{s["wallets"]:>3} wallets: {s["completed"]:>4} ok / {s["started"]:>4} started, '
          f'{s["throughput_per_min"]}/min, unexpected {s["unexpected"]}, '
          f'total p50 {L["p50"]} p99 {L["p99"]} ms, confirm p50 {s["latency_ms"]["confirmMs"]["p50"]}, '
          f'index p50 {s["latency_ms"]["indexMs"]["p50"]}, momentum mean {s["chain"]["interval"]["mean"]}s '
          f'max {s["chain"]["interval"]["max"]}s, rebuild p50 {s["env"]["evm_rebuild_ms"]["p50"]}ms, '
          f'price max {s["chain"]["fusion_price_max"]}, fund {s["fund_s"]}s')
    for k, v in s["unexpected_by_cause"].items():
        print(f"      {v:>4} × {k}")
print("totals", json.dumps(summary["totals"]))
