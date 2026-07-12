# These are not decoration: they are the deploy workflow's inputs (#7).
# Everything the GitHub Actions job needs to authenticate, push, and roll out
# is derived here rather than pasted into the workflow, so renaming a
# resource cannot silently leave a stale string behind in YAML.
#
#   dagger call terraform --project=<id> --owner-email=<you> \
#     --access-token=cmd://"gcloud auth print-access-token" output

output "service_url" {
  description = "The Cloud Run service's HTTPS URL."
  value       = google_cloud_run_v2_service.app.uri
}

output "service_name" {
  description = "Cloud Run service name, for `gcloud run deploy`."
  value       = google_cloud_run_v2_service.app.name
}

output "region" {
  description = "Region of the Cloud Run service and the registry."
  value       = var.region
}

output "registry" {
  description = <<-EOT
    The registry prefix the image pipeline publishes to — exactly what the
    Dagger module's `ci --registry=` expects, with no repository or tag
    appended.
  EOT
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.containers.repository_id}"
}

output "runtime_service_account" {
  description = "Identity the app runs as."
  value       = google_service_account.runtime.email
}

output "deployer_service_account" {
  description = "Identity GitHub Actions impersonates; the `service_account` input of google-github-actions/auth."
  value       = google_service_account.deployer.email
}

output "workload_identity_provider" {
  description = <<-EOT
    Full resource name of the WIF provider; the `workload_identity_provider`
    input of google-github-actions/auth.
  EOT
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "oauth_client_secret" {
  description = "Secret Manager secret that holds the Google Sign-In client secret. Terraform creates the secret; the owner adds the version."
  value       = google_secret_manager_secret.oauth_client_secret.secret_id
}
