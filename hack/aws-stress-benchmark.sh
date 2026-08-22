#!/usr/bin/env bash
# Latency/throughput benchmark: leader-aware client vs round_robin, full
# stress suite, one AWS cluster. Each repetition runs the palindrome order
# official, custom, custom, official so slow time drift cancels. Per leg the
# script snapshots etcd_network_peer_sent_bytes_total on every member before
# and after, and parses each scenario's request count and p99 from the
# runner's SCENARIO PASSED lines into a final comparison table.
#
# Required environment: AWS_REGION, ETCD_INFRA_AWS_VPC, ETCD_INFRA_AWS_AMI,
# ETCD_INFRA_AWS_INSTANCE_PROFILE (optional: ETCD_INFRA_AWS_SUBNET,
# ETCD_INFRA_AWS_SECURITY_GROUPS, ETCD_INFRA_AWS_VERSION) — same as
# hack/aws-conformance-stress-e2e.sh. Credentials: the least-privilege user
# from hack/aws-e2e.iam-policy.json.
#
# Optional:
#   ETCD_INFRA_BENCH_REPS            palindrome repetitions (default: 1 →
#                                    four legs A B B A)
#   ETCD_INFRA_AWS_STRESS_DURATION   seconds per scenario per leg (default: 90)
#   ETCD_INFRA_AWS_STRESS_WORKERS    workers (default: 10)
#   ETCD_INFRA_AWS_STRESS_RPS        requests/s per worker (default: 100)
#   ETCD_INFRA_SLOW_PATH_MULTIPLIER  latency-budget multiplier (default: 2)
#   ETCD_INFRA_BENCH_SCENARIOS       comma-separated scenario IDs (default: all)
#   ETCD_INFRA_AWS_BASTION_TYPE      bastion instance type (default: t3a.nano)
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="etcd-infra-aws-stress-benchmark"

for var in ETCD_INFRA_AWS_VPC ETCD_INFRA_AWS_AMI ETCD_INFRA_AWS_INSTANCE_PROFILE; do
    if [[ -z "${!var:-}" ]]; then
        echo "${var} is required" >&2
        exit 2
    fi
done
command -v aws >/dev/null 2>&1 || { echo "the AWS CLI is required" >&2; exit 2; }
command -v session-manager-plugin >/dev/null 2>&1 || { echo "session-manager-plugin is required" >&2; exit 2; }

tmpdir="$(mktemp -d)"
tunnel_pid=""
cleanup() {
    if [[ -n "${tunnel_pid}" ]]; then
        kill "${tunnel_pid}" >/dev/null 2>&1 || true
    fi
    "${project_root}/bin/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 \
        || echo "WARN: aws down failed for ${cluster}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
    if [[ "${1:-0}" != "0" ]]; then
        # Keep the leg logs on failure: they carry the scenario output that
        # explains why the run died.
        local kept="/tmp/etcd-infra-bench-failed-$(date +%s)"
        mv "${tmpdir}" "${kept}"
        echo "benchmark failed; leg logs kept at ${kept}" >&2
        return
    fi
    rm -rf "${tmpdir}"
}
# bash 3.2 leaks the trap's last command status over the failing status;
# capture and re-exit so the caller sees the real result.
trap 'rc=$?; cleanup "$rc"; exit "$rc"' EXIT

# Build both binaries up front so nothing rebuilds between legs.
ETCD_INFRA_CLIENT=official "${project_root}/hack/build.sh"
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra-official"
ETCD_INFRA_CLIENT=custom "${project_root}/hack/build.sh"
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra-custom"

"${project_root}/bin/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 || true

up_args=(
    --name "${cluster}" --members 3 --bastion
    --bastion-instance-type "${ETCD_INFRA_AWS_BASTION_TYPE:-t3a.nano}"
    --vpc "${ETCD_INFRA_AWS_VPC}"
    --ami "${ETCD_INFRA_AWS_AMI}"
    --instance-profile "${ETCD_INFRA_AWS_INSTANCE_PROFILE}"
    --version "${ETCD_INFRA_AWS_VERSION:-latest}"
    --dry-run=false
)
if [[ -n "${AWS_REGION:-}" ]]; then
    up_args+=(--region "${AWS_REGION}")
fi
if [[ -n "${ETCD_INFRA_AWS_SUBNET:-}" ]]; then
    up_args+=(--subnet "${ETCD_INFRA_AWS_SUBNET}")
fi
if [[ -n "${ETCD_INFRA_AWS_SECURITY_GROUPS:-}" ]]; then
    up_args+=(--security-groups "${ETCD_INFRA_AWS_SECURITY_GROUPS}")
fi
"${project_root}/bin/etcd-infra" aws up "${up_args[@]}"

"${project_root}/bin/etcd-infra" aws tunnel --name "${cluster}" \
    > "${tmpdir}/endpoints" 2> "${tmpdir}/tunnel.log" &
tunnel_pid=$!
for _ in $(seq 1 90); do
    [[ -s "${tmpdir}/endpoints" ]] && break
    if ! kill -0 "${tunnel_pid}" 2>/dev/null; then
        echo "aws tunnel exited before the endpoints were ready:" >&2
        cat "${tmpdir}/tunnel.log" >&2
        exit 1
    fi
    sleep 1
done
endpoints="$(cat "${tmpdir}/endpoints")"
if [[ -z "${endpoints}" ]]; then
    echo "aws tunnel never printed endpoints; log:" >&2
    cat "${tmpdir}/tunnel.log" >&2
    exit 1
fi
echo "benchmark endpoints via bastion: ${endpoints}"

total_sent() { awk '/^TOTAL/ {print $2}' "$1"; }

run_leg() {
    local label="$1" rep="$2" binary="${tmpdir}/etcd-infra-$3"
    "${binary}" metrics --endpoints "${endpoints}" > "${tmpdir}/${label}-${rep}-before.txt"
    # bash 3.2 (macOS) rejects an empty array expansion under set -u.
    local scenario_args=()
    if [[ -n "${ETCD_INFRA_BENCH_SCENARIOS:-}" ]]; then
        scenario_args+=(--scenario "${ETCD_INFRA_BENCH_SCENARIOS}")
    fi
    ETCD_INFRA_SLOW_PATH_MULTIPLIER="${ETCD_INFRA_SLOW_PATH_MULTIPLIER:-2}" \
    "${binary}" stress --endpoints "${endpoints}" \
        ${scenario_args[@]+"${scenario_args[@]}"} \
        --duration "${ETCD_INFRA_AWS_STRESS_DURATION:-90}" \
        --workers "${ETCD_INFRA_AWS_STRESS_WORKERS:-10}" \
        --rps "${ETCD_INFRA_AWS_STRESS_RPS:-100}" \
        > "${tmpdir}/${label}-${rep}.log" 2>&1
    "${binary}" metrics --endpoints "${endpoints}" > "${tmpdir}/${label}-${rep}-after.txt"
    local delta=$(( $(total_sent "${tmpdir}/${label}-${rep}-after.txt") - $(total_sent "${tmpdir}/${label}-${rep}-before.txt") ))
    echo "${delta}" > "${tmpdir}/${label}-${rep}-peerbytes.txt"
    echo "leg ${label} rep ${rep}: peer-sent delta ${delta} bytes"
}

reps="${ETCD_INFRA_BENCH_REPS:-1}"
for rep in $(seq 1 "${reps}"); do
    run_leg official "${rep}" official
    run_leg custom "${rep}a" custom
    run_leg custom "${rep}b" custom
    run_leg official "${rep}z" official
done

python3 - "${tmpdir}" <<'PYEOF'
import glob, json, os, re, sys

tmpdir = sys.argv[1]
legs = {}
for path in sorted(glob.glob(os.path.join(tmpdir, "*-peerbytes.txt"))):
    label = os.path.basename(path).replace("-peerbytes.txt", "")
    legs[label] = legs.get(label, {}) 
    legs[label]["peer_bytes"] = int(open(path).read().strip())
    legs[label]["scenarios"] = {}
    logpath = path.replace("-peerbytes.txt", ".log")
    for line in open(logpath, errors="replace"):
        line = line.strip()
        if not line.startswith("{") or "SCENARIO PASSED" not in line:
            continue
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        if rec.get("msg") != "SCENARIO PASSED":
            continue
        took = rec.get("took", "")
        mm = re.match(r"(?:(\d+)m)?([\d.]+)s", took)
        secs = (int(mm.group(1)) * 60 if mm and mm.group(1) else 0) + (float(mm.group(2)) if mm else 0)
        legs[label]["scenarios"][rec["scenario"]] = {
            "requests": rec.get("requests", 0),
            "p99_ms": rec.get("p99_ms", 0),
            "avg_ms": rec.get("avg_ms", 0),
            "secs": secs,
        }

def pool(legs, prefix):
    out = {"peer_bytes": 0, "scenarios": {}}
    n = 0
    for label, data in legs.items():
        if not label.startswith(prefix):
            continue
        n += 1
        out["peer_bytes"] += data["peer_bytes"]
        for sc, m in data["scenarios"].items():
            cur = out["scenarios"].setdefault(sc, {"requests": 0, "p99_ms": 0.0, "avg_ms": 0.0, "secs": 0.0, "legs": 0})
            cur["requests"] += m["requests"]
            cur["p99_ms"] += m["p99_ms"]
            cur["avg_ms"] += m["avg_ms"]
            cur["secs"] += m["secs"]
            cur["legs"] += 1
    if n:
        out["peer_bytes"] /= n
    for cur in out["scenarios"].values():
        cur["p99_ms"] /= max(cur["legs"], 1)
        cur["avg_ms"] /= max(cur["legs"], 1)
    return out

off = pool(legs, "official")
cust = pool(legs, "custom")

print()
print("=== stress benchmark: leader-aware (custom) vs round_robin (official) ===")
print(f"peer-sent bytes per leg (mean over palindrome legs):")
print(f"  official: {off['peer_bytes']:.0f}")
print(f"  custom:   {cust['peer_bytes']:.0f}")
if off["peer_bytes"] > 0:
    red = (off["peer_bytes"] - cust["peer_bytes"]) * 100 / off["peer_bytes"]
    print(f"  reduction: {red:.1f}%")
print()
print(f"{'SCENARIO':40s} {'ops/s off':>10s} {'ops/s la':>10s} {'p99 off':>9s} {'p99 la':>9s} {'avg off':>9s} {'avg la':>9s}")
for sc in sorted(set(off["scenarios"]) | set(cust["scenarios"])):
    o = off["scenarios"].get(sc)
    c = cust["scenarios"].get(sc)
    if not o or not c:
        continue
    def ops(m):
        return m["requests"] / m["secs"] if m["secs"] else 0
    print(f"{sc:40s} {ops(o):10.1f} {ops(c):10.1f} {o['p99_ms']:9.0f} {c['p99_ms']:9.0f} {o['avg_ms']:9.0f} {c['avg_ms']:9.0f}")
PYEOF
