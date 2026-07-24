# Deployment

This directory contains configuration-as-code to deploy Tessera to supported infrastructure:
 - `modules`: terraform modules to configure infrastructure for running a Tessera log.
   + `gcp`: a Tessera GCP specific terraform module.
   + `aws`: a Tessera AWS specific terraform module.
 - `live`: example terragrunt configurations for deploying to different environments which use the modules.

## Prerequisites

Deploying these examples requires installation of:
 - [`terraform`](https://developer.hashicorp.com/terraform/install) or 
   [`opentofu`](https://opentofu.org/docs/intro/install/)
 - [`terragrunt`](https://terragrunt.gruntwork.io/docs/getting-started/install/)

## Deploying

See individual `live` subdirectories.

## Enabling the GCP conformance lane

The [`gcp_gcs_conformance.yml`](../.github/workflows/gcp_gcs_conformance.yml)
workflow runs `cmd/conformance/objstore` against **real** GCS + Cloud SQL (MySQL),
mirroring the AWS lane. It is **opt-in**: the job is skipped unless a maintainer
has provisioned the keyless identity plumbing below and set the repo Variables, so
an unconfigured fork is never gated on infra it doesn't have.

Everything is keyless — GitHub Actions federates to GCP via Workload Identity
Federation (WIF); there are no long-lived service-account keys.

The infrastructure is split into two Terragrunt stacks:

- **`live/gcp/ci-identity`** (module `modules/gcp/ci_identity`) — **persistent,
  applied once, never destroyed.** WIF pool + GitHub OIDC provider (locked to a
  single `owner/repo` via `assertion.repository`), the CI service account the
  workflow impersonates, and its project roles (Storage admin, Cloud SQL admin +
  client, log writer). The pool, provider, and
  service account carry `lifecycle { prevent_destroy = true }` because **deleting
  a WIF pool reserves its name for 30 days**, which would lock CI out for a month.
- **`live/gcp/ci-mysql`** (module `modules/gcp/ci_mysql`) — **ephemeral.** A small
  Cloud SQL MySQL 8.0 instance (`deletion_protection = false`) with a built-in
  application user and the log database. The conformance workflow applies it at
  the start of each run and destroys it in an `always()` step, so it is never
  leaked. You do not apply this by hand for CI.

### One-time bootstrap (a human, as project Owner)

1. **Choose a GCP project** and export the environment the stacks read:

   ```bash
   export GOOGLE_PROJECT=<your-gcp-project-id>
   export GOOGLE_REGION=us-central1
   export GITHUB_REPOSITORY=patflynn/trillian-tessera   # or transparency-dev/tessera upstream
   ```

2. **Apply the identity stack** (it enables the required APIs, creates the WIF
   pool/provider, the CI service account, and grants its roles):

   ```bash
   terragrunt apply --terragrunt-working-dir deployment/live/gcp/ci-identity
   ```

3. **Set the repo Variables** (Settings → Secrets and variables → Actions →
   Variables — these are Variables, **not** secrets) from the stack outputs:

   ```bash
   terragrunt output --terragrunt-working-dir deployment/live/gcp/ci-identity

   gh variable set GCP_PROJECT_ID          --body "<project_id>"
   gh variable set GCP_REGION              --body "<region>"
   gh variable set GCP_WIF_PROVIDER        --body "<wif_provider>"
   gh variable set GCP_CI_SA_EMAIL         --body "<ci_sa_email>"
   gh variable set GCP_CONFORMANCE_ENABLED --body "true"
   ```

   | Variable                  | Purpose                                                         |
   | ------------------------- | --------------------------------------------------------------- |
   | `GCP_CONFORMANCE_ENABLED` | `"true"` enables the lane; unset/`false` skips it.              |
   | `GCP_PROJECT_ID`          | Project hosting the GCS bucket and Cloud SQL instance.          |
   | `GCP_REGION`              | Region for the per-run bucket and Cloud SQL instance.          |
   | `GCP_WIF_PROVIDER`        | Full Workload Identity provider resource name (keyless auth).   |
   | `GCP_CI_SA_EMAIL`         | CI service account email the workflow impersonates.            |

That's it — a `workflow_dispatch` (or a push to `main`) now runs the lane,
standing up the ephemeral Cloud SQL instance and GCS bucket, exercising the
objstore binary + hammer, and tearing everything down afterwards. Enabling the
lane does **not** run it on every PR push; a PR opts in by adding the
**`run-gcs-conformance`** label (mirroring the AWS lane below), so unlabelled PRs
don't stand up real GCP infrastructure. The `ci-mysql` stack is managed entirely
by the workflow; the `ci-identity` stack is **never** part of a teardown.

> [!WARNING]
> **Adding the `run-gcs-conformance` label runs the PR's own workflow code with
> the powerful CI service account** (`roles/cloudsql.admin` +
> `roles/storage.admin`). A `pull_request`-triggered run executes the workflow
> and build steps *as they appear on the PR branch*, so labeling an untrusted PR
> effectively hands its author those permissions in the CI project. Maintainers
> must review a PR's workflow, build, and dependency changes **before** applying
> the label, and only label PRs they trust. The same applies to the
> `run-aws-conformance` label below.

### Portability to upstream

Nothing above hard-codes a project or account: the project/region come from
`GOOGLE_PROJECT`/`GOOGLE_REGION` (fed by the repo Variables), and the WIF provider
is locked to `GITHUB_REPOSITORY` (auto-set in Actions; required with no default
when applying locally, so it can never silently bind to the wrong repo). To
enable the lane on `transparency-dev/tessera`, run the same bootstrap in an
upstream-owned project with `GITHUB_REPOSITORY=transparency-dev/tessera`.

### The AWS conformance lane

The AWS lane ([`aws_integration_test.yml`](../.github/workflows/aws_integration_test.yml))
still runs on push to `main`. It can also be run before merge via
`workflow_dispatch`, or on a PR by adding the **`run-aws-conformance`** label (or
setting `vars.AWS_CONFORMANCE_ENABLED` to `"true"`). Unlabelled PRs do not stand
up real AWS infrastructure.

