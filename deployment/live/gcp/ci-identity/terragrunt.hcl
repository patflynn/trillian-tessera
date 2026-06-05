terraform {
  source = "${get_repo_root()}/deployment/modules/gcp//ci_identity"
}

locals {
  project_id = get_env("GOOGLE_PROJECT")
  location   = get_env("GOOGLE_REGION", "us-central1")
}

remote_state {
  backend = "gcs"

  config = {
    project  = local.project_id
    location = local.location
    bucket   = "${local.project_id}-ci-identity-terraform-state"
    prefix   = "ci-identity/terraform.tfstate"

    gcs_bucket_labels = {
      name = "terraform_state_storage"
    }
  }
}

inputs = {
  project_id = local.project_id
  region     = local.location
}
