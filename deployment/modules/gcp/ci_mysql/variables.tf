variable "project_id" {
  description = "GCP project ID that hosts the fork CI infrastructure"
  type        = string
}

variable "region" {
  description = "Region in which to create the Cloud SQL instance"
  type        = string
  default     = "us-central1"
}

variable "ci_sa_email" {
  description = "Email of the CI service account (from the ci-identity stack output)"
  type        = string
}

variable "instance_name" {
  description = "Name of the Cloud SQL instance"
  type        = string
  default     = "tessera-ci-mysql"
}

variable "database_version" {
  description = "Cloud SQL database engine version"
  type        = string
  default     = "MYSQL_8_0"
}

variable "tier" {
  description = "Machine tier for the Cloud SQL instance"
  type        = string
  default     = "db-g1-small"
}
