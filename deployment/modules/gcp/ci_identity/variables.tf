variable "project_id" {
  description = "GCP project ID that hosts the conformance CI infrastructure"
  type        = string
}

variable "region" {
  description = "Region used for the CI infrastructure and derived resource names"
  type        = string
  default     = "us-central1"
}

variable "github_repository" {
  description = "owner/repo this Workload Identity provider is locked to. A WIF security boundary, so it is required with no default: an unset value must fail rather than silently lock CI to another repo."
  type        = string
}

variable "pool_id" {
  description = "Workload Identity Pool ID"
  type        = string
  default     = "github-pool"
}

variable "provider_id" {
  description = "Workload Identity Pool Provider ID"
  type        = string
  default     = "github-provider"
}

variable "ci_sa_account_id" {
  description = "Account ID (not email) of the CI service account"
  type        = string
  default     = "tessera-ci"
}
