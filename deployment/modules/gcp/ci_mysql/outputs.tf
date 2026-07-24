output "connection_name" {
  description = "Cloud SQL connection name (project:region:instance) for the Cloud SQL Auth Proxy"
  value       = google_sql_database_instance.mysql.connection_name
}

output "instance_name" {
  description = "Name of the Cloud SQL instance"
  value       = google_sql_database_instance.mysql.name
}

output "db_name" {
  description = "Name of the log database"
  value       = google_sql_database.log.name
}

output "db_user" {
  description = "Built-in MySQL application user"
  value       = google_sql_user.app.name
}

output "db_password" {
  description = "Password for the application user (ephemeral, lives only in this run's state)"
  value       = local.db_password
  sensitive   = true
}
