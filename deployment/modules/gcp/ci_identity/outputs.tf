output "wif_provider" {
  description = "Full WIF provider resource name for google-github-actions/auth (set as the GCP_WIF_PROVIDER repo variable)"
  value       = google_iam_workload_identity_pool_provider.github_provider.name
}

output "ci_sa_email" {
  description = "Email of the CI service account (set as the GCP_CI_SA_EMAIL repo variable)"
  value       = google_service_account.tessera_ci.email
}

output "project_id" {
  description = "GCP project ID (set as the GCP_PROJECT_ID repo variable)"
  value       = var.project_id
}

output "project_number" {
  description = "GCP project number"
  value       = data.google_project.this.number
}

output "region" {
  description = "Region for the CI infrastructure (set as the GCP_REGION repo variable)"
  value       = var.region
}
