variable "project_id" {
  description = "Google Cloud project that owns every resource here."
  type        = string
}

variable "region" {
  description = "Region for Cloud Run and Artifact Registry."
  type        = string
  default     = "us-central1"
}

variable "firestore_location" {
  description = <<-EOT
    Firestore location. A multi-region ("nam5", "eur3") or a region
    ("us-central1"). It is fixed for the life of the database — Firestore
    cannot be moved, only recreated, and the event log is the source of
    truth, so treat this as write-once.
  EOT
  type        = string
  default     = "nam5"
}

variable "owner_email" {
  description = <<-EOT
    The single Google account allowed to use the app. It is both the app's
    allowlist (OWNER_EMAIL) and — until the service is opened up — the one
    principal with roles/run.invoker on it.
  EOT
  type        = string

  validation {
    condition     = can(regex("^[^@[:space:]]+@[^@[:space:]]+\\.[^@[:space:]]+$", var.owner_email))
    error_message = "owner_email must be an email address."
  }
}

variable "oauth_client_id" {
  description = <<-EOT
    The Google Sign-In (OAuth 2.0) client id the app authenticates people
    with. Create the client by hand in the Cloud console — an OAuth client is
    tied to a consent screen, a brand, and a support email, none of which are
    infrastructure — and pass its id here. Its secret goes to Secret Manager,
    out of band; this is the half that is public.

    Required, and deliberately without a default: the app refuses to boot
    without it (see internal/config), so a plan that quietly supplied an empty
    string would trade a loud failure here for a revision that will not start.

    The client's authorized redirect URI must be the service's own URL plus
    /auth/callback — Google will not redirect anywhere it was not told about.
  EOT
  type        = string

  validation {
    condition     = can(regex("\\.apps\\.googleusercontent\\.com$", var.oauth_client_id))
    error_message = "oauth_client_id must be a Google OAuth client id, ending in .apps.googleusercontent.com."
  }
}

variable "service_name" {
  description = "Cloud Run service name; also the prefix for the service accounts and the secret."
  type        = string
  default     = "expense-tracker"
}

variable "artifact_registry_repository" {
  description = "Artifact Registry repository id that holds the app images."
  type        = string
  default     = "containers"
}

variable "image" {
  description = <<-EOT
    The image the Cloud Run service is created with — the placeholder, so
    that `terraform apply` can stand up an empty environment before any app
    image exists (the registry it would be published to is created by this
    same apply).

    Terraform does NOT track the image after that: the deploy pipeline (#7)
    publishes a tag and rolls it out with `gcloud run deploy`, and run.tf
    ignores subsequent changes. So overriding this only affects the first
    apply, and a later `terraform apply` will not revert a deployed image
    back to the placeholder.
  EOT
  type        = string
  default     = "us-docker.pkg.dev/cloudrun/container/hello"
}

variable "allow_unauthenticated" {
  description = <<-EOT
    Grant roles/run.invoker to allUsers.

    Off, and it must stay off until the app authenticates its own users
    (#13/#14): the app has no auth middleware yet, so a public service is an
    unauthenticated read/write handle on the event log. Google Sign-In is an
    in-app OIDC flow, though — a browser cannot complete it against a service
    that rejects unauthenticated requests at the edge — so this flips to true
    in the story that lands the middleware, not before.

    Until then the owner reaches the service with an identity token:

        gcloud run services proxy expense-tracker --region <region>
  EOT
  type        = bool
  default     = false
}

variable "max_instances" {
  description = "Cloud Run ceiling. A single-user app that scales to zero; this is a cost fuse, not capacity planning."
  type        = number
  default     = 2
}

variable "github_repository" {
  description = <<-EOT
    The "owner/name" whose GitHub Actions runs may impersonate the deployer
    service account through Workload Identity Federation. It is the trust
    boundary of the whole keyless deploy: any workflow in this repository can
    mint a token for that account, and no workflow outside it can.
  EOT
  type        = string
  default     = "Zaba505/expense-tracker"

  validation {
    # GitHub's actual character set, not merely "has a slash in it". The value
    # is interpolated into the CEL condition and the principalSet in wif.tf,
    # and the narrowest thing that can be true of a trust boundary is the best
    # thing to assert about it. It also rejects the likeliest typo — pasting
    # the URL rather than the repository.
    condition     = can(regex("^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+$", var.github_repository))
    error_message = "github_repository must be in owner/name form, using only the characters GitHub allows: letters, digits and hyphen in the owner; also underscore and period in the name."
  }
}
