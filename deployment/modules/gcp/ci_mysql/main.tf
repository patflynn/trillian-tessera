terraform {
  backend "gcs" {}

  required_providers {
    # Bare source addresses so tofu resolves them from registry.opentofu.org,
    # matching the committed .terraform.lock.hcl. A fully-qualified
    # registry.terraform.io source would leave plugins and lock file inconsistent
    # and abort `tofu apply`.
    google = {
      source  = "hashicorp/google"
      version = "6.1.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "3.6.3"
    }
  }

  # A range so the CI workflow's tofu and local/flake tofu both work.
  required_version = ">= 1.9.8, < 2.0.0"
}

# Password for the built-in application user. The workflow supplies a masked,
# per-run value via var.db_password; standalone use generates a random one. It
# lives only in this stack's per-run state (a sensitive output). `special = false`
# keeps it alphanumeric so it embeds in a MySQL DSN without escaping.
resource "random_password" "db" {
  count   = var.db_password == "" ? 1 : 0
  length  = 24
  special = false
}

locals {
  db_password = var.db_password != "" ? var.db_password : random_password.db[0].result
}

##
## Ephemeral Cloud SQL (MySQL) instance.
##
## Created and destroyed per run: no prevent_destroy, deletion_protection off.
##
resource "google_sql_database_instance" "mysql" {
  project          = var.project_id
  name             = var.instance_name
  region           = var.region
  database_version = var.database_version

  # Must be destroyable at the end of every run.
  deletion_protection = false

  settings {
    tier              = var.tier
    edition           = "ENTERPRISE"
    disk_size         = 10
    disk_autoresize   = true
    availability_type = "ZONAL"
  }
}

# The built-in application user. Cloud SQL Admin API users get cloudsqlsuperuser,
# so this user is fully privileged on the log database below.
resource "google_sql_user" "app" {
  project  = var.project_id
  instance = google_sql_database_instance.mysql.name
  name     = var.db_user
  host     = "%"
  password = local.db_password
}

# The log database the conformance server coordinates through.
resource "google_sql_database" "log" {
  project  = var.project_id
  instance = google_sql_database_instance.mysql.name
  name     = var.db_name
}
