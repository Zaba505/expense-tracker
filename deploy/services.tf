# The APIs every other file here depends on.
#
# Enabling an API is slow and eventually consistent: a resource created in
# the same apply as its API routinely fails with "API has not been used in
# project ... before or it is disabled". Every resource below therefore
# depends_on this one, which is also why it is its own file — it is a
# prerequisite, not a peer.

locals {
  services = [
    "artifactregistry.googleapis.com", # the image repository
    "firestore.googleapis.com",        # the event log
    "iam.googleapis.com",              # the service accounts
    "iamcredentials.googleapis.com",   # WIF: minting tokens for them
    "run.googleapis.com",              # the app
    "secretmanager.googleapis.com",    # the OIDC client secret
    "sts.googleapis.com",              # WIF: exchanging the GitHub token
  ]
}

resource "google_project_service" "this" {
  for_each = toset(local.services)

  project = var.project_id
  service = each.value

  # A destroy of this root module tears down an environment; it has no
  # business turning APIs off at the project level, which would break
  # anything else in the project that happens to use them.
  disable_on_destroy = false
}
