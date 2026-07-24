terraform {
  backend "gcs" {}

  required_providers {
    google = {
      source  = "registry.terraform.io/hashicorp/google"
      version = "6.1.0"
    }
  }

  # A range (not an exact pin) so the CI workflow's tofu and local/flake tofu
  # both work.
  required_version = ">= 1.9.8, < 2.0.0"
}

# The project number is needed to construct the Workload Identity principalSet
# member that binds this repo's OIDC identities to the CI service account.
data "google_project" "this" {
  project_id = var.project_id
}

locals {
  # Roles the CI service account needs. cloudsql.admin (not just client) is
  # required because the workflow creates and destroys the Cloud SQL instance.
  ci_sa_roles = [
    "roles/storage.admin",   # per-run GCS bucket + terragrunt state bucket
    "roles/cloudsql.admin",  # create/destroy the ephemeral instance + its user
    "roles/cloudsql.client", # connect through the Cloud SQL Auth Proxy
    "roles/logging.logWriter",
    # roles/iam.serviceAccountTokenCreator is deliberately NOT granted: the
    # workflow only needs the CI SA's own token via WIF impersonation (the
    # wif_user binding below). Granting it at project scope would let the SA mint
    # tokens for every SA in the project; if ever needed, scope it to the SA.
  ]
}

##
## APIs the lane depends on.
##
resource "google_project_service" "apis" {
  for_each = toset([
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "sts.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "storage.googleapis.com",
    "sqladmin.googleapis.com",
    "logging.googleapis.com",
  ])

  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

##
## Workload Identity Federation: keyless GitHub Actions -> GCP.
##
resource "google_iam_workload_identity_pool" "github_pool" {
  project                   = var.project_id
  workload_identity_pool_id = var.pool_id
  display_name              = "GitHub Actions"
  description               = "Keyless OIDC federation for ${var.github_repository} CI"

  # Deleting a WIF pool reserves its name for 30 days, blocking recreation and
  # locking CI out. This identity stack must survive any `destroy`.
  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.apis]
}

resource "google_iam_workload_identity_pool_provider" "github_provider" {
  project                            = var.project_id
  workload_identity_pool_id          = google_iam_workload_identity_pool.github_pool.workload_identity_pool_id
  workload_identity_pool_provider_id = var.provider_id
  display_name                       = "GitHub OIDC"

  attribute_mapping = {
    "google.subject"             = "assertion.sub"
    "attribute.repository"       = "assertion.repository"
    "attribute.repository_owner" = "assertion.repository_owner"
    "attribute.ref"              = "assertion.ref"
  }

  # The security boundary: only OIDC tokens minted for this exact repo are
  # accepted, so a fork of this repo cannot federate into the project.
  attribute_condition = "assertion.repository=='${var.github_repository}'"

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }

  lifecycle {
    prevent_destroy = true
  }
}

##
## CI service account the workflows impersonate.
##
resource "google_service_account" "tessera_ci" {
  project      = var.project_id
  account_id   = var.ci_sa_account_id
  display_name = "Tessera conformance CI"

  lifecycle {
    prevent_destroy = true
  }
}

# Let WIF identities from this repo impersonate the CI service account.
resource "google_service_account_iam_member" "wif_user" {
  service_account_id = google_service_account.tessera_ci.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/projects/${data.google_project.this.number}/locations/global/workloadIdentityPools/${google_iam_workload_identity_pool.github_pool.workload_identity_pool_id}/attribute.repository/${var.github_repository}"
}

# Project-level roles for the CI service account.
resource "google_project_iam_member" "ci_sa_roles" {
  for_each = toset(local.ci_sa_roles)

  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${google_service_account.tessera_ci.email}"
}
