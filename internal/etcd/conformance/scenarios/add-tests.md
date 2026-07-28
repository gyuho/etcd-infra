# Adding a new test

Follow these three steps to add a new etcd conformance scenario.

> [!NOTE]
> **Key files in this directory:**
>
> - [`ids.go`](./ids.go) — the authoritative registry of all scenario IDs and their human-readable descriptions. Every scenario must be registered here before implementation. The generator reads this file to produce scenario stubs and the README catalogue.
> - [`gen.sh`](./gen.sh) — the code generator script. It reads `ids.go` and produces: a stub implementation file for each new scenario, updated mapping helpers, and a regenerated `README.md` with the current scenario count and ID list. Always run this after editing `ids.go`.

**Step 1: Register the scenario ID in ids.go**

Add a new constant and description entry in [`ids.go`](./ids.go). Use UPPER_SNAKE_CASE for the ID, matching the naming convention of existing scenarios. The description should be a concise phrase that explains what etcd behavior the scenario validates.

**Step 2: Regenerate test files with gen.sh**

Run [`./gen.sh`](./gen.sh) from this directory. The script produces:

- A new stub file `scenario_[lower_snake_id].go` with the scenario skeleton.
- Updated mapping helpers that include the new ID.
- A regenerated `README.md` with the updated scenario count and ID list.

**Step 3: Implement the scenario logic**

Open the generated file `scenario_[lower_snake_id].go` and implement the test body. Follow the patterns established by existing scenarios: use the shared client setup, assert etcd behavior via the standard assertion helpers, and keep the scenario focused on a single etcd API surface or behavioral property.
