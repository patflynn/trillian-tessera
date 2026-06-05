# CI MySQL stack (ephemeral — torn down nightly)

This Terragrunt config provisions the **ephemeral** Cloud SQL (MySQL 8.0)
instance the conformance lane runs against, plus the IAM database user for the
CI service account (keyless / passwordless DB auth).

Unlike the [ci-identity](../ci-identity/README.md) stack, this one is **meant to
be destroyed and recreated freely**. `deletion_protection = false` and there is
no `prevent_destroy` lifecycle. A nightly GitHub Action destroys it to cap spend;
recreate it on demand.

See [`deployment/README.md`](../../../README.md) for the full bootstrap order.

## Environment variables it reads

| Variable              | Required | Default       | Purpose                                      |
| --------------------- | -------- | ------------- | -------------------------------------------- |
| `GOOGLE_PROJECT`      | yes      | —             | Project ID (state bucket + instance project) |
| `GOOGLE_REGION`       | no       | `us-central1` | Region for the instance                      |
| `TESSERA_CI_SA_EMAIL` | yes      | —             | CI SA email (from the ci-identity output)    |

`TESSERA_CI_SA_EMAIL` is the `ci_sa_email` output of the ci-identity stack; the
module strips the `.gserviceaccount.com` suffix to form the IAM DB username.

## Teardown / recreate

- **Automatic teardown:** `.github/workflows/ci_infra_teardown.yml` runs nightly
  (09:00 UTC) and on `workflow_dispatch`.
- **Recreate:** trigger `.github/workflows/ci_infra_apply.yml`
  (`workflow_dispatch` only).
- **Locally:**

  ```bash
  export GOOGLE_PROJECT=tessera-498520
  export GOOGLE_REGION=us-central1
  export TESSERA_CI_SA_EMAIL=tessera-ci@tessera-498520.iam.gserviceaccount.com

  nix develop -c terragrunt apply   --terragrunt-working-dir deployment/live/gcp/ci-mysql
  nix develop -c terragrunt destroy --terragrunt-working-dir deployment/live/gcp/ci-mysql
  ```

## Outputs

- `connection_name` — `project:region:instance` for the Cloud SQL Auth Proxy.
- `instance_name` — the Cloud SQL instance name.
