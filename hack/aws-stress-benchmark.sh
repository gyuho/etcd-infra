#!/usr/bin/env bash
# Latency/throughput benchmark: leader-aware client vs round_robin, full
# stress suite, one AWS cluster. Suites execute on the cluster's stress
# client instances inside the VPC ("etcd-infra aws drive"); with more than
# one stress client they spread across the VPC's subnets so the load is
# balanced across availability zones. Each repetition runs the palindrome
# order official, custom, custom, official so slow time drift cancels.
# Results come back through S3 as JSON lines plus peer-metric snapshots,
# parsed into the comparison table below.
#
# Required environment: AWS_REGION, ETCD_INFRA_AWS_VPC, ETCD_INFRA_AWS_AMI,
# ETCD_INFRA_AWS_INSTANCE_PROFILE (optional: ETCD_INFRA_AWS_SUBNET,
# ETCD_INFRA_AWS_SECURITY_GROUPS, ETCD_INFRA_AWS_VERSION,
# ETCD_INFRA_AWS_S3_BUCKET) — same as hack/aws-conformance-stress-e2e.sh.
# Credentials: the least-privilege user from hack/aws-e2e.iam-policy.json.
#
# Optional:
#   ETCD_INFRA_BENCH_REPS            palindrome repetitions (default: 1 →
#                                    four legs A B B A)
#   ETCD_INFRA_BENCH_SCENARIOS       comma-separated scenario IDs (default: all)
#   ETCD_INFRA_AWS_STRESS_CLIENTS    driver instance count (default: 1)
#   ETCD_INFRA_AWS_STRESS_DURATION   seconds per scenario per leg (default: 90)
#   ETCD_INFRA_AWS_STRESS_WORKERS    workers per client (default: 10)
#   ETCD_INFRA_AWS_STRESS_RPS        requests/s per worker (default: 100)
#   ETCD_INFRA_SLOW_PATH_MULTIPLIER  latency-budget multiplier (default: 1; in-VPC needs none)
#   ETCD_INFRA_AWS_BASTION_TYPE      stress client instance type (default: t3a.medium)
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

. "${project_root}/hack/aws-e2e-lib.sh"

tmpdir="$(mktemp -d)"
cleanup() {
    if [[ -x "${tmpdir}/etcd-infra" ]]; then
        "${tmpdir}/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 \
            || echo "WARN: aws down failed for ${cluster}; cluster may leak — check ~/.etcd-infra/aws/ and EC2" >&2
    fi
    rm -rf "${tmpdir}"
}
trap 'rc=$?; cleanup "$rc"; exit "$rc"' EXIT

"${project_root}/hack/build.sh"
cp "${project_root}/bin/etcd-infra" "${tmpdir}/etcd-infra"
bucket="$(ensure_aws_bucket)"
build_linux_driver_binaries "${tmpdir}"

"${tmpdir}/etcd-infra" aws down --name "${cluster}" >/dev/null 2>&1 || true

up_args=(
    --name "${cluster}" --members 3
    --stress-clients "${ETCD_INFRA_AWS_STRESS_CLIENTS:-1}"
    --bastion-instance-type "${ETCD_INFRA_AWS_BASTION_TYPE:-t3a.medium}"
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
"${tmpdir}/etcd-infra" aws up "${up_args[@]}"

results_root="/tmp/etcd-infra-bench-${cluster}-$(date +%s)"

run_leg() {
    local label="$1" binary="$2"
    local args="--duration ${ETCD_INFRA_AWS_STRESS_DURATION:-90} --workers ${ETCD_INFRA_AWS_STRESS_WORKERS:-10} --rps ${ETCD_INFRA_AWS_STRESS_RPS:-100}"
    if [[ -n "${ETCD_INFRA_BENCH_SCENARIOS:-}" ]]; then
        args="${args} --scenario ${ETCD_INFRA_BENCH_SCENARIOS}"
    fi
    # A leg that fails a threshold must not kill the palindrome: the
    # comparison table (and its fails column) is the product, and the
    # remaining legs still carry measurement value.
    if ! "${tmpdir}/etcd-infra" aws drive --name "${cluster}" \
        --binary "${binary}" --bucket "${bucket}" \
        --suite stress --args "${args}" \
        --env "ETCD_INFRA_SLOW_PATH_MULTIPLIER=${ETCD_INFRA_SLOW_PATH_MULTIPLIER:-2}" \
        --results-dir "${results_root}/${label}" \
        --timeout 4h; then
        echo "leg ${label} FAILED (see ${results_root}/${label}); continuing" >&2
    fi
    echo "leg ${label} done"
}

reps="${ETCD_INFRA_BENCH_REPS:-1}"
for rep in $(seq 1 "${reps}"); do
    run_leg "official-${rep}"  "${tmpdir}/etcd-infra-official"
    run_leg "custom-${rep}a"   "${tmpdir}/etcd-infra-custom"
    run_leg "custom-${rep}b"   "${tmpdir}/etcd-infra-custom"
    run_leg "official-${rep}z" "${tmpdir}/etcd-infra-official"
done

python3 - "${results_root}" <<'PYEOF'
import glob, json, os, re, sys

root = sys.argv[1]

def parse_duration(s):
    # Go duration strings: "1.605s", "405ms", "2m8.4s"
    m = re.match(r"(?:(\d+)m)?([\d.]+)s$", s)
    if m: return (int(m.group(1))*60 if m.group(1) else 0) + float(m.group(2))
    m = re.match(r"([\d.]+)ms$", s)
    if m: return float(m.group(1)) / 1000
    return 0.0

legs = {}
for legdir in sorted(glob.glob(os.path.join(root, "*"))):
    label = os.path.basename(legdir)
    if not os.path.isdir(legdir):
        continue
    peer = 0
    scenarios = {}
    for clientdir in sorted(glob.glob(os.path.join(legdir, "*"))):
        def rd(name):
            try: return int(open(os.path.join(clientdir, name)).read().strip())
            except Exception: return -1
        b, a = rd("metrics-before.txt"), rd("metrics-after.txt")
        if b >= 0 and a >= 0:
            peer += a - b
        try:
            for line in open(os.path.join(clientdir, "results.jsonl"), errors="replace"):
                line = line.strip()
                if not line.startswith("{"): continue
                rec = json.loads(line)
                sc = rec["scenario"]
                cur = scenarios.setdefault(sc, {"requests": 0, "p99": 0.0, "avg": 0.0, "wavg": 0.0, "secs": 0.0, "n": 0, "failed": 0, "buckets": None})
                cur["requests"] += rec.get("totalRequests", 0)
                cur["p99"] += parse_duration(rec.get("p99Latency", "0s"))
                cur["avg"] += parse_duration(rec.get("averageLatency", "0s"))
                cur["wavg"] += parse_duration(rec.get("averageLatency", "0s")) * rec.get("totalRequests", 0)
                cur["secs"] += parse_duration(rec.get("took", "0s"))
                cur["n"] += 1
                # Mergeable latency histogram (see metrics.go): counts sum
                # across clients and runs, so aggregated percentiles come
                # from every request, not from a mean of per-run percentiles.
                if rec.get("latencyBuckets"):
                    if cur["buckets"] is None:
                        cur["buckets"] = [0] * len(rec["latencyBuckets"])
                    for i, count in enumerate(rec["latencyBuckets"]):
                        cur["buckets"][i] += count
                if not rec.get("success", False):
                    cur["failed"] += 1
        except FileNotFoundError:
            pass
    legs[label] = {"peer": peer, "scenarios": scenarios}

def pool(prefix):
    out = {"peer": 0, "scenarios": {}}
    n = 0
    for label, data in legs.items():
        if not label.startswith(prefix): continue
        n += 1
        out["peer"] += data["peer"]
        for sc, m in data["scenarios"].items():
            cur = out["scenarios"].setdefault(sc, {"requests": 0, "p99": 0.0, "avg": 0.0, "wavg": 0.0, "secs": 0.0, "n": 0, "failed": 0, "buckets": None})
            for k in ("requests", "p99", "avg", "wavg", "secs", "n", "failed"):
                cur[k] += m[k]
            if m["buckets"] is not None:
                if cur["buckets"] is None:
                    cur["buckets"] = [0] * len(m["buckets"])
                for i, count in enumerate(m["buckets"]):
                    cur["buckets"][i] += count
    if n: out["peer"] /= n
    return out

off = pool("official")
cust = pool("custom")

print()
print("=== stress benchmark: leader-aware (custom) vs round_robin (official) ===")
print("peer-sent bytes per leg (mean over palindrome legs, all clients):")
print(f"  official: {off['peer']:.0f}")
print(f"  custom:   {cust['peer']:.0f}")
if off["peer"] > 0 and cust["peer"] > 0:
    print(f"  reduction: {(off['peer']-cust['peer'])*100/off['peer']:.1f}%")
else:
    print("  (incomplete: a side has no leg data — check for failed downloads above)")
print()
print(f"{'SCENARIO':40s} {'ops/s off':>10s} {'ops/s la':>10s} {'p99s off':>9s} {'p99s la':>9s} {'avgs off':>9s} {'avgs la':>9s} {'fails':>6s}")
for sc in sorted(set(off["scenarios"]) | set(cust["scenarios"])):
    o = off["scenarios"].get(sc)
    c = cust["scenarios"].get(sc)
    if not o or not c: continue
    def ops(m): return m["requests"] / m["secs"] if m["secs"] else 0
    def lat(m, k):
        # With the mergeable histogram, the p99 is the upper bound of the
        # bucket holding the 99th-percentile request across all runs (about
        # 9% resolution), and the average is request-weighted. Without it
        # (older result files), fall back to the mean of per-run values.
        if k == "p99" and m["buckets"]:
            total = sum(m["buckets"])
            threshold, cumulative = total * 0.99, 0
            for i, count in enumerate(m["buckets"]):
                cumulative += count
                if cumulative >= threshold:
                    return 0.0625 * 2 ** ((i + 1) / 8) / 1000
        if k == "avg" and m["requests"]:
            return m["wavg"] / m["requests"]
        return m[k] / m["n"] if m["n"] else 0
    fails = o["failed"] + c["failed"]
    print(f"{sc:40s} {ops(o):10.1f} {ops(c):10.1f} {lat(o,'p99'):9.2f} {lat(c,'p99'):9.2f} {lat(o,'avg'):9.2f} {lat(c,'avg'):9.2f} {fails:6d}")
PYEOF

echo "results: ${results_root}"
