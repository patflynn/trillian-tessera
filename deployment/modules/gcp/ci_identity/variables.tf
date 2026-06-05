variable "project_id" {
  description = "GCP project ID that hosts the fork CI infrastructure"
  type        = string
}

variable "region" {
  description = "Region used for derived resource names (e.g. the Cloud SQL connection name)"
  type        = string
  default     = "us-central1"
}

variable "github_repository" {
  description = "The owner/repo this Workload Identity provider is locked to"
  type        = string
  default     = "patflynn/trillian-tessera"
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

variable "sql_instance_name" {
  description = "Name of the ephemeral Cloud SQL instance, used to compute the connection name output"
  type        = string
  default     = "tessera-ci-mysql"
}
