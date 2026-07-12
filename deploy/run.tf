# The app.
#
# Terraform owns the SERVICE — its identity, its configuration, its probes,
# its scaling — and deliberately does not own the IMAGE. The deploy pipeline
# (#7) publishes a tag and rolls it out with `gcloud run deploy`, so the
# running image changes many times between applies. If Terraform tracked it,
# every apply after a deploy would plan a rollback to whatever tag the config
# last mentioned, and one `terraform apply` run to change an unrelated
# variable would quietly redeploy an old build. Hence the ignore_changes
# below: two tools, one resource, but a clean split of what each owns.
#
# The consequence to keep in mind: `var.image` is the image this service is
# CREATED with and nothing more. To change what is running, deploy it.
resource "google_cloud_run_v2_service" "app" {
  project     = var.project_id
  location    = var.region
  name        = var.service_name
  description = "Event-sourced expense tracker (github.com/Zaba505/expense-tracker)"

  # The edge accepts requests from the internet; who may actually invoke the
  # service is decided by the IAM bindings below, not here. Locking ingress
  # down as well would only cut off the owner's browser, which is the one
  # client this app has.
  ingress = "INGRESS_TRAFFIC_ALL"

  # The service is disposable — its state lives in Firestore, which is
  # delete-protected (firestore.tf), and its image lives in the registry.
  # Destroy it and a re-apply plus a re-deploy brings it back exactly.
  deletion_protection = false

  template {
    service_account = google_service_account.runtime.email

    # Scale to zero: a single-user app is idle almost always, and a warm
    # instance is a bill for nothing. The price is a cold start on the first
    # request after a quiet spell — a static Go binary on a scratch image,
    # so that is a container pull and a process, not a runtime booting.
    scaling {
      min_instance_count = 0
      max_instance_count = var.max_instances
    }

    containers {
      image = var.image

      # Cloud Run injects PORT with this value; the app reads it (config's
      # PORT, defaulting to 8080). They agree, and the smoke test in the
      # Dagger module is what keeps them agreeing.
      ports {
        container_port = 8080
      }

      env {
        name  = "GCP_PROJECT"
        value = var.project_id
      }

      env {
        name  = "OWNER_EMAIL"
        value = var.owner_email
      }

      # No FIRESTORE_EMULATOR_HOST here, which is the entire difference
      # between this and the local run configuration: unset, the app
      # authenticates to native Firestore with the runtime account's ADC.

      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }

        # Bill for CPU only while a request is in flight, and boost it during
        # the cold start the scale-to-zero above guarantees.
        cpu_idle          = true
        startup_cpu_boost = true
      }

      # Cloud Run has no continuous readiness concept — it has a startup
      # probe, which gates traffic, and a liveness probe, which restarts the
      # container. That maps exactly onto the app's two probes, and mapping
      # them the other way round is the classic mistake: a liveness probe
      # that touched Firestore would turn a database blip into a restart
      # loop, which cannot fix a database.
      #
      # So: readiness (a real Firestore round-trip) decides when a new
      # revision may take traffic...
      startup_probe {
        http_get {
          path = "/health/readiness"
        }

        # The app bounds its own check at web.ReadinessTimeout (3s) and
        # answers 503 with a reason when it blows. Giving the probe 5s means
        # that answer arrives and is logged, instead of the probe timing out
        # first and reporting nothing but silence.
        timeout_seconds = 5
        period_seconds  = 10

        # A minute for the first Firestore connection of a cold container.
        # Generous on purpose: the cost of being wrong here is a failed
        # rollout, and the cost of waiting is ten seconds of an empty
        # revision nobody is using yet.
        failure_threshold = 6
      }

      # ...and liveness (which touches nothing) decides when the process is
      # wedged and only a restart will do.
      liveness_probe {
        http_get {
          path = "/health/liveness"
        }

        timeout_seconds   = 3
        period_seconds    = 30
        failure_threshold = 3
      }
    }
  }

  lifecycle {
    ignore_changes = [
      # Owned by `gcloud run deploy` — see the note at the top.
      template[0].containers[0].image,

      # gcloud stamps these on every deploy ("gcloud", "5xx.y.z"). They are
      # metadata about who last touched the service, so a diff on them is
      # Terraform noticing it was not gcloud — which is true, and not worth a
      # plan.
      client,
      client_version,
    ]
  }

  depends_on = [
    google_project_service.this,
    google_project_iam_member.runtime_datastore,
  ]
}

# The owner can always reach the service, holding an identity token:
#
#   gcloud run services proxy expense-tracker --region us-central1
#
# This is what makes a service with no auth of its own usable — and it is why
# allow_unauthenticated can stay false until the app authenticates people
# itself.
resource "google_cloud_run_v2_service_iam_member" "owner_invoker" {
  project  = var.project_id
  location = google_cloud_run_v2_service.app.location
  name     = google_cloud_run_v2_service.app.name
  role     = "roles/run.invoker"
  member   = "user:${var.owner_email}"
}

# Public, once — and only once — the app checks who is calling. See the
# variable's own comment: this is the switch the auth story flips, and
# nothing before it should.
resource "google_cloud_run_v2_service_iam_member" "public_invoker" {
  count = var.allow_unauthenticated ? 1 : 0

  project  = var.project_id
  location = google_cloud_run_v2_service.app.location
  name     = google_cloud_run_v2_service.app.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
