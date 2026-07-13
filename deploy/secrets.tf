# The app's two secrets: the Google Sign-In client secret, and the key that
# signs session cookies.
#
# The containers are created here; the values are not. Terraform never sees
# either one — a secret passed through a variable ends up in plaintext in the
# state file, which defeats the purpose of Secret Manager. The owner adds each
# version out of band, once (see deploy/README.md).
#
# Both are now referenced by the Cloud Run service (run.tf), which is what the
# auth story (#13) changed. The consequence is worth stating plainly, because
# the failure is otherwise mystifying: a container that mounts a secret with
# no versions does not start. Adding the two versions is therefore a
# prerequisite of the next deploy, not a nicety.

# What the app presents to Google to redeem an authorization code. Google
# generates it with the OAuth client; it cannot be regenerated locally.
#
#   gcloud secrets versions add expense-tracker-oauth-client-secret \
#     --project <project> --data-file=-
resource "google_secret_manager_secret" "oauth_client_secret" {
  project   = var.project_id
  secret_id = "${var.service_name}-oauth-client-secret"

  replication {
    auto {}
  }

  depends_on = [google_project_service.this]
}

# What signs the session cookie. It is not a password and has no counterpart
# anywhere — it is 32 bytes of randomness, and anyone holding it can mint a
# cookie claiming to be the owner, which is why it lives here and not in an
# environment variable somebody once pasted.
#
#   openssl rand -base64 32 | gcloud secrets versions add \
#     expense-tracker-session-key --project <project> --data-file=-
#
# Rotating it is a version away, and costs nothing but a sign-in: sessions
# signed with the old key stop verifying, which is exactly what revoking every
# session means for an app with no session store.
resource "google_secret_manager_secret" "session_key" {
  project   = var.project_id
  secret_id = "${var.service_name}-session-key"

  replication {
    auto {}
  }

  depends_on = [google_project_service.this]
}

# The runtime account may read both, and nothing else may. Cloud Run resolves
# the secret as this account at instance start, so without these the revision
# comes up unable to read its own configuration.
resource "google_secret_manager_secret_iam_member" "runtime_accessor" {
  project   = var.project_id
  secret_id = google_secret_manager_secret.oauth_client_secret.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.runtime.member
}

resource "google_secret_manager_secret_iam_member" "runtime_session_key_accessor" {
  project   = var.project_id
  secret_id = google_secret_manager_secret.session_key.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = google_service_account.runtime.member
}
