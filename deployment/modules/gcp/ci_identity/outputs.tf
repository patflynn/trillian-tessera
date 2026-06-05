output "wif_provider" {
  description = "Full WIF provider resource name for google-github-actions/auth (GCP_WIF_PROVIDER repo variable)"
  value       = google_iam_workload_identity_pool_provider.github_provider.name
}

output "ci_sa_email" {
  description = "Email of the CI service account (GCP_CI_SA_EMAIL repo variable)"
  value       = google_service_account.tessera_ci.email
}

output "project_id" {
  description = "GCP project ID (GCP_PROJECT_ID repo variable)"
  value       = var.project_id
}

output "project_number" {
  description = "GCP project number"
  value       = data.google_project.this.number
}

output "region" {
  description = "Region for the CI infrastructure (GCP_REGION repo variable)"
  value       = var.region
}

output "sql_connection_name" {
  description = "Deterministic Cloud SQL connection name for the ephemeral instance"
  value       = "${var.project_id}:${var.region}:${var.sql_instance_name}"
}

output "sql_iam_user" {
  description = "Cloud SQL IAM database username (CI SA email minus the .gserviceaccount.com suffix)"
  value       = local.sql_iam_user
}
