# Two service accounts, and nothing they do not need.
#
#   runtime   what the app runs as on Cloud Run. It reads and appends the
#             event log, and later reads the OIDC client secret. That is the
#             whole of it.
#   deployer  what a GitHub Actions run impersonates (see wif.tf). It pushes
#             an image and rolls out a revision. It cannot touch the data.
#
# The split is the point: a compromised CI run can deploy a bad revision —
# which is recoverable, that is what revisions are for — but it cannot read
# or delete the expense history, because nothing it can impersonate has
# datastore access. Neither account is granted a project-wide role beyond the
# one Firestore role the app cannot work without.

resource "google_service_account" "runtime" {
  project      = var.project_id
  account_id   = "${var.service_name}-run"
  display_name = "expense-tracker Cloud Run runtime"
  description  = "Identity the app runs as; reads and appends the Firestore event log."

  depends_on = [google_project_service.this]
}

# roles/datastore.user, not roles/datastore.owner: the app reads and writes
# documents. It never creates indexes, imports, exports, or drops a database
# — and an append-only log is exactly the thing you do not want a runtime
# identity able to administer.
resource "google_project_iam_member" "runtime_datastore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = google_service_account.runtime.member
}

resource "google_service_account" "deployer" {
  project      = var.project_id
  account_id   = "${var.service_name}-deployer"
  display_name = "expense-tracker CI deployer"
  description  = "Impersonated by GitHub Actions via Workload Identity Federation; publishes images and deploys revisions."

  depends_on = [google_project_service.this]
}

# Push (and pull) images — scoped to this repository, not the project's
# registries at large.
resource "google_artifact_registry_repository_iam_member" "deployer_writer" {
  project    = var.project_id
  location   = google_artifact_registry_repository.containers.location
  repository = google_artifact_registry_repository.containers.name
  role       = "roles/artifactregistry.writer"
  member     = google_service_account.deployer.member
}

# Deploy revisions of the service. roles/run.developer is project-scoped
# because `gcloud run deploy` on a service that does not exist yet needs to
# be able to create it; a service-scoped binding cannot express that.
resource "google_project_iam_member" "deployer_run" {
  project = var.project_id
  role    = "roles/run.developer"
  member  = google_service_account.deployer.member
}

# The one that is easy to miss and fails the deploy with a bewildering
# error: to deploy a revision that RUNS AS the runtime account, the deployer
# must be allowed to act as it. Scoped to that single account, so it grants
# the right to deploy this app and no right to impersonate anything else.
resource "google_service_account_iam_member" "deployer_actas_runtime" {
  service_account_id = google_service_account.runtime.name
  role               = "roles/iam.serviceAccountUser"
  member             = google_service_account.deployer.member
}
