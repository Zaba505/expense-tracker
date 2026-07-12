# Where the images CI publishes land.
#
# The z5labs GoApp pipeline tags every build <shortSha>-<commitISO>, so tags
# are never reused and the repository only grows — one image per push to
# main, forever. The cleanup policies below are what stops that from
# becoming a slowly growing storage bill for images nobody can roll back to
# anyway.
#
# KEEP is evaluated ahead of DELETE, so the two together read as: keep the
# last 10 images no matter how old, and drop anything else past 30 days.
resource "google_artifact_registry_repository" "containers" {
  project       = var.project_id
  location      = var.region
  repository_id = var.artifact_registry_repository
  format        = "DOCKER"
  description   = "expense-tracker container images, published by the z5labs GoApp pipeline"

  cleanup_policy_dry_run = false

  cleanup_policies {
    id     = "keep-last-10"
    action = "KEEP"

    most_recent_versions {
      keep_count = 10
    }
  }

  cleanup_policies {
    id     = "delete-older-than-30d"
    action = "DELETE"

    condition {
      older_than = "2592000s" # 30 days
    }
  }

  depends_on = [google_project_service.this]
}
