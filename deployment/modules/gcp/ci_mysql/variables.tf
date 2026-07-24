variable "project_id" {
  description = "GCP project ID that hosts the conformance CI infrastructure"
  type        = string
}

variable "region" {
  description = "Region in which to create the Cloud SQL instance"
  type        = string
  default     = "us-central1"
}

variable "instance_name" {
  description = "Name of the Cloud SQL instance. The CI lane overrides this per-run so runs never collide on the name (Cloud SQL reserves it for a while after deletion)."
  type        = string
  default     = "tessera-ci-mysql"
}

variable "database_version" {
  description = "Cloud SQL database engine version"
  type        = string
  default     = "MYSQL_8_0"
}

variable "tier" {
  description = "Machine tier for the Cloud SQL instance (small/cheap by default)"
  type        = string
  default     = "db-g1-small"
}

variable "db_name" {
  description = "Name of the log database created on the instance"
  type        = string
  default     = "tessera"
}

variable "db_user" {
  description = "Name of the built-in MySQL application user"
  type        = string
  default     = "tessera"
}

variable "db_password" {
  description = "Password for the built-in application user. If empty, a random one is generated (surfaced via the db_password output). The CI workflow supplies a masked, per-run value."
  type        = string
  default     = ""
  sensitive   = true
}
