terraform {
  backend "gcs" {}

  required_providers {
    google = {
      source  = "registry.terraform.io/hashicorp/google"
      version = "6.1.0"
    }
  }

  required_version = "= 1.9.8"
}

# The project number is needed to construct the Workload Identity principalSet
# member and is exported for the consumer (the ci-mysql stack + repo variables).
data "google_project" "this" {
  project_id = var.project_id
}

locals {
  # The CI service account email, derived deterministically from the account id.
  ci_sa_email = "${var.ci_sa_account_id}@${var.project_id}.iam.gserviceaccount.com"

  # Cloud SQL IAM database usernames are the SA email WITHOUT the
  # ".gserviceaccount.com" suffix (a Cloud SQL quirk).
  sql_iam_user = trimsuffix(local.ci_sa_email, ".gserviceaccount.com")

  # Roles the CI service account needs. cloudsql.admin (not just client) is
  # required because the GitHub Action creates and DELETES the SQL instance.
  ci_sa_roles = [
    "roles/storage.admin",
    "roles/cloudsql.admin",
    "roles/cloudsql.client",
    "roles/cloudsql.instanceUser",
    "roles/logging.logWriter",
  ]
}

##
## APIs (runbook §2)
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
## Workload Identity Federation: keyless GitHub Actions -> GCP (runbook §3)
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
  # accepted. External-fork PRs never receive such a token.
  attribute_condition = "assertion.repository=='${var.github_repository}'"

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }

  lifecycle {
    prevent_destroy = true
  }
}

##
## CI service account the workflows impersonate (runbook §3b)
##
resource "google_service_account" "tessera_ci" {
  project      = var.project_id
  account_id   = var.ci_sa_account_id
  display_name = "Tessera fork CI"

  lifecycle {
    prevent_destroy = true
  }
}

# Let WIF identities from this repo impersonate the CI service account (§3c).
resource "google_service_account_iam_member" "wif_user" {
  service_account_id = google_service_account.tessera_ci.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/projects/${data.google_project.this.number}/locations/global/workloadIdentityPools/${google_iam_workload_identity_pool.github_pool.workload_identity_pool_id}/attribute.repository/${var.github_repository}"
}

# Project-level roles for the CI service account (§3d).
resource "google_project_iam_member" "ci_sa_roles" {
  for_each = toset(local.ci_sa_roles)

  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${google_service_account.tessera_ci.email}"
}
