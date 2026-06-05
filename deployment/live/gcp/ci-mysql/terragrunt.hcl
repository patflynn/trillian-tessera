terraform {
  source = "${get_repo_root()}/deployment/modules/gcp//ci_mysql"
}

locals {
  project_id  = get_env("GOOGLE_PROJECT")
  location    = get_env("GOOGLE_REGION", "us-central1")
  ci_sa_email = get_env("TESSERA_CI_SA_EMAIL")
}

remote_state {
  backend = "gcs"

  config = {
    project  = local.project_id
    location = local.location
    bucket   = "${local.project_id}-ci-mysql-terraform-state"
    prefix   = "ci-mysql/terraform.tfstate"

    gcs_bucket_labels = {
      name = "terraform_state_storage"
    }
  }
}

inputs = {
  project_id  = local.project_id
  region      = local.location
  ci_sa_email = local.ci_sa_email
}
