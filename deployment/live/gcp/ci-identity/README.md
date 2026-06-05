# CI identity stack (persistent — NEVER torn down)

This Terragrunt config provisions the **keyless** identity plumbing the fork's CI
uses to reach GCP: a Workload Identity Federation (WIF) pool + GitHub OIDC
provider locked to `patflynn/trillian-tessera`, the `tessera-ci` service account
the workflows impersonate, and that account's project roles. It also enables the
required APIs.

It is applied **once** by a human acting as project Owner and then effectively
never changes. The WIF pool, provider, and service account carry
`lifecycle { prevent_destroy = true }` because **deleting a WIF pool reserves its
name for 30 days**, which would lock CI out for a month.

See [`docs/design/gcp-ci-setup.md`](../../../../docs/design/gcp-ci-setup.md) for
the design and [`deployment/README.md`](../../../README.md) for the full
bootstrap order.

## Environment variables it reads

| Variable          | Required | Default       | Purpose                                  |
| ----------------- | -------- | ------------- | ---------------------------------------- |
| `GOOGLE_PROJECT`  | yes      | —             | Project ID (state bucket + provider)     |
| `GOOGLE_REGION`   | no       | `us-central1` | Region (derived connection-name output)  |

## Apply (human, as Owner)

```bash
export GOOGLE_PROJECT=tessera-498520
export GOOGLE_REGION=us-central1

nix develop -c terragrunt apply --terragrunt-working-dir deployment/live/gcp/ci-identity
```

Then read the outputs and set them as repo **variables** (not secrets):

```bash
nix develop -c terragrunt output --terragrunt-working-dir deployment/live/gcp/ci-identity

gh variable set GCP_WIF_PROVIDER --body "<wif_provider>"
gh variable set GCP_CI_SA_EMAIL  --body "<ci_sa_email>"
gh variable set GCP_PROJECT_ID   --body "<project_id>"
gh variable set GCP_REGION       --body "<region>"
```

## Outputs

- `wif_provider` — full provider resource name for `google-github-actions/auth`.
- `ci_sa_email` — CI service account email (and the apply/teardown workflows' SA).
- `project_id`, `project_number`, `region`.
- `sql_connection_name` — `project:region:tessera-ci-mysql`.
- `sql_iam_user` — Cloud SQL IAM username (SA email minus `.gserviceaccount.com`).
