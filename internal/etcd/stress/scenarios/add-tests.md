# Adding a new stress workload

Follow these three steps to add a new etcd stress workload.

**Step 1: Register the workload ID in ids.go**

Add a new constant and description entry in [`ids.go`](./ids.go). Use UPPER_SNAKE_CASE for the ID, matching the naming convention of existing workloads. The description should be a concise phrase that explains which Kubernetes etcd usage pattern the workload targets.

**Step 2: Regenerate README and mapping helpers with gen.sh**

Run [`./gen.sh`](./gen.sh) from this directory. The script produces:

- A regenerated `README.md` with the updated workload count and ID list.
- Updated mapping helpers that include the new workload ID.

**Step 3: Implement the workload logic**

Create `scenario_[lower_snake_id].go` and implement the workload body following the existing patterns.

> [!TIP]
> Study the existing runner hook patterns before implementing a new workload. Each workload uses a consistent structure: `NewClient` for etcd client setup, metric recording hooks for throughput and latency measurement, and runner lifecycle hooks (`Setup`, `Run`, `Teardown`) that integrate with the stress harness. Matching this structure ensures your workload's results appear correctly in the aggregate report and that client connections are properly managed across workload iterations.
