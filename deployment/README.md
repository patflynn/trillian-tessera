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

## Fork CI infrastructure (keyless, GCP)

The `patflynn/trillian-tessera` fork runs its own CI on a personal GCP project
(`tessera-498520`) with **no long-lived keys** — GitHub Actions authenticate via
Workload Identity Federation (WIF). The infra is split into two stacks so that
the expensive, ephemeral database can be torn down nightly while the identity
plumbing stays put:

- **`live/gcp/ci-identity`** (module `modules/gcp/ci_identity`) — **persistent,
  never destroyed.** WIF pool + GitHub OIDC provider (locked to this repo via
  `assertion.repository=='patflynn/trillian-tessera'`), the `tessera-ci` service
  account, and its project roles. The pool, provider, and service account carry
  `lifecycle { prevent_destroy = true }` because **deleting a WIF pool reserves
  its name for 30 days**, which would lock CI out for a month.
- **`live/gcp/ci-mysql`** (module `modules/gcp/ci_mysql`) — **ephemeral.** A
  Cloud SQL MySQL 8.0 instance (`deletion_protection = false`) plus the IAM DB
  user. Created and destroyed at will.

### Environment variables

| Stack         | Reads                                                  |
| ------------- | ------------------------------------------------------ |
| `ci-identity` | `GOOGLE_PROJECT`, `GOOGLE_REGION`                      |
| `ci-mysql`    | `GOOGLE_PROJECT`, `GOOGLE_REGION`, `TESSERA_CI_SA_EMAIL` |

### Bootstrap order (one-time, human as project Owner)

1. **Apply `ci-identity` locally** as Owner:
   `nix develop -c terragrunt apply --terragrunt-working-dir deployment/live/gcp/ci-identity`.
2. **Set four repo Variables** (not secrets) from its outputs:
   `GCP_WIF_PROVIDER`, `GCP_CI_SA_EMAIL`, `GCP_PROJECT_ID`, `GCP_REGION`.
3. **Apply `ci-mysql`** — either run the `CI Infra Apply` workflow
   (`workflow_dispatch`) or, locally with `TESSERA_CI_SA_EMAIL` exported,
   `nix develop -c terragrunt apply --terragrunt-working-dir deployment/live/gcp/ci-mysql`.

### Teardown / recreate (routine)

The ephemeral DB is managed by two keyless GitHub Actions workflows, both
consuming the repo Variables above (never secrets):

- `.github/workflows/ci_infra_teardown.yml` — destroys `ci-mysql` nightly
  (09:00 UTC) and on `workflow_dispatch`.
- `.github/workflows/ci_infra_apply.yml` — recreates `ci-mysql` on
  `workflow_dispatch` only (no schedule, to avoid surprise spend).

Either can also be run locally with `terragrunt apply` / `terragrunt destroy`
against the `ci-mysql` stack. The `ci-identity` stack is **never** part of a
teardown.

See [`docs/design/gcp-ci-setup.md`](../docs/design/gcp-ci-setup.md) for the
underlying design and the equivalent raw `gcloud` runbook.

