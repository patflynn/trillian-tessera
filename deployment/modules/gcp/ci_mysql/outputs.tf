output "connection_name" {
  description = "Cloud SQL connection name (project:region:instance) for the Auth Proxy"
  value       = google_sql_database_instance.tessera_ci_mysql.connection_name
}

output "instance_name" {
  description = "Name of the Cloud SQL instance"
  value       = google_sql_database_instance.tessera_ci_mysql.name
}
