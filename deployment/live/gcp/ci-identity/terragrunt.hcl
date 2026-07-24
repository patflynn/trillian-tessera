terraform {
  source = "${get_repo_root()}/deployment/modules/gcp//ci_identity"
}

locals {
  project_id = get_env("GOOGLE_PROJECT")
  location   = get_env("GOOGLE_REGION", "us-central1")

  # GitHub Actions sets GITHUB_REPOSITORY to "owner/repo"; export it manually when
  # applying locally. No default: it is a WIF security boundary, so an unset value
  # must fail rather than bind CI to another repo.
  github_repository = get_env("GITHUB_REPOSITORY")
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
  project_id        = local.project_id
  region            = local.location
  github_repository = local.github_repository
}
