terraform {
  backend "gcs" {}

  required_providers {
    google = {
      # Bare source so OpenTofu resolves the provider consistently between
      # `init` and `apply` under Terragrunt's provider cache (see ci_identity).
      source  = "hashicorp/google"
      version = "6.1.0"
    }
  }

  # See ci_identity/main.tf: a range (not the conformance module's exact
  # "= 1.9.8") so the apply/teardown workflows and local tofu all work.
  required_version = ">= 1.9.8, < 2.0.0"
}

locals {
  # Cloud SQL IAM database usernames are the SA email WITHOUT the
  # ".gserviceaccount.com" suffix.
  sql_iam_user = trimsuffix(var.ci_sa_email, ".gserviceaccount.com")
}

##
## Ephemeral Cloud SQL (MySQL) instance (runbook §4).
##
## This stack is destroyed nightly and recreated on demand, so it carries NO
## prevent_destroy lifecycle and deletion_protection is disabled.
##
resource "google_sql_database_instance" "tessera_ci_mysql" {
  project          = var.project_id
  name             = var.instance_name
  region           = var.region
  database_version = var.database_version

  # Must be destroyable nightly.
  deletion_protection = false

  settings {
    tier              = var.tier
    edition           = "ENTERPRISE"
    disk_size         = 10
    disk_autoresize   = true
    availability_type = "ZONAL"

    # Keyless DB auth: the runner connects via the Auth Proxy with IAM auth.
    database_flags {
      name  = "cloudsql_iam_authentication"
      value = "on"
    }
  }
}

# IAM database user for the CI service account (passwordless / keyless).
resource "google_sql_user" "ci_iam_user" {
  project  = var.project_id
  instance = google_sql_database_instance.tessera_ci_mysql.name
  name     = local.sql_iam_user
  type     = "CLOUD_IAM_SERVICE_ACCOUNT"
}
