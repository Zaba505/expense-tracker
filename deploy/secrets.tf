# The Google Sign-In client secret.
#
# The container is created here; the value is not. Terraform never sees it —
# a secret passed through a variable ends up in plaintext in the state file,
# which defeats the purpose of Secret Manager. The owner adds the version out
# of band, once:
#
#   gcloud secrets versions add expense-tracker-oauth-client-secret \
#     --project <project> --data-file=-
#
# Nothing reads it yet, and Cloud Run deliberately does not reference it (see
# run.tf): a container that mounts a secret with no versions fails to start,
# so wiring it in before there is a value to wire would break the deploy for
# the sake of a story that has not landed. The auth story (#13) adds the env
# var; this gives it somewhere to add it to, and the runtime account the
# right to read it.
resource "google_secret_manager_secret" "oauth_client_secret" {
  project   = var.project_id
  secret_id = "${var.service_name}-oauth-client-secret"

  replication {
    auto {}
  }

  depends_on = [google_project_service.this]
}

resource "google_secret_manager_secret_iam_member" "runtime_accessor" {
  project   = var.project_id
  secret_id = google_secret_manager_secret.oauth_client_secret.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.runtime.member
}
