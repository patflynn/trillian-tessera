terraform {
  source = "${get_repo_root()}/deployment/modules/gcp//ci_mysql"
}

locals {
  project_id = get_env("GOOGLE_PROJECT")
  location   = get_env("GOOGLE_REGION", "us-central1")

  # TESSERA_CI_RUN_ID scopes both the instance name and the tfstate prefix so
  # concurrent runs and re-runs never collide (Cloud SQL reserves a deleted name
  # for a while). Empty when run standalone, giving a single stable stack.
  run_id        = get_env("TESSERA_CI_RUN_ID", "")
  suffix        = local.run_id == "" ? "" : "-${local.run_id}"
  instance_name = "tessera-ci-mysql${local.suffix}"
  state_prefix  = local.run_id == "" ? "ci-mysql/terraform.tfstate" : "ci-mysql/${local.run_id}/terraform.tfstate"
}

remote_state {
  backend = "gcs"

  config = {
    project  = local.project_id
    location = local.location
    bucket   = "${local.project_id}-ci-mysql-terraform-state"
    prefix   = local.state_prefix

    gcs_bucket_labels = {
      name = "terraform_state_storage"
    }
  }
}

inputs = {
  project_id    = local.project_id
  region        = local.location
  instance_name = local.instance_name
}
