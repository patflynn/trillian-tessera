# CI MySQL stack (ephemeral — created and destroyed per run)

This Terragrunt config provisions the **ephemeral** Cloud SQL (MySQL 8.0)
instance the conformance lane runs against, plus a built-in application user
(with a generated, per-run password) and the log database.

Unlike the [ci-identity](../ci-identity/README.md) stack, this one is **meant to
be destroyed and recreated freely**. `deletion_protection = false` and there is
no `prevent_destroy` lifecycle. The
[`gcp_gcs_conformance.yml`](../../../../.github/workflows/gcp_gcs_conformance.yml)
workflow applies it at the start of a run and destroys it in an `always()` step,
so an instance is never leaked.

Because Cloud SQL reserves a deleted instance name for a while, the workflow sets
`TESSERA_CI_RUN_ID` so both the instance name and the tfstate prefix are unique
per run; concurrent PR runs and re-runs never collide.

See [`deployment/README.md`](../../../README.md#enabling-the-gcp-conformance-lane)
for the full bootstrap order.

## Environment variables it reads

| Variable            | Required | Default            | Purpose                                                  |
| ------------------- | -------- | ------------------ | -------------------------------------------------------- |
| `GOOGLE_PROJECT`    | yes      | —                  | Project ID (state bucket + instance project)            |
| `GOOGLE_REGION`     | no       | `us-central1`      | Region for the instance                                 |
| `TESSERA_CI_RUN_ID` | no       | `` (empty)         | Per-run suffix for the instance name and tfstate prefix |

## Apply / destroy locally

```bash
export GOOGLE_PROJECT=<your-gcp-project-id>
export GOOGLE_REGION=us-central1

terragrunt apply   --terragrunt-working-dir deployment/live/gcp/ci-mysql
terragrunt destroy --terragrunt-working-dir deployment/live/gcp/ci-mysql
```

## Outputs

- `connection_name` — `project:region:instance` for the Cloud SQL Auth Proxy.
- `instance_name` — the Cloud SQL instance name.
- `db_name`, `db_user` — the log database and application user.
- `db_password` — generated application-user password (sensitive; masked in CI).
