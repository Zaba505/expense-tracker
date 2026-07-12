# Workload Identity Federation: how GitHub Actions deploys without a key.
#
# The alternative — a service account JSON key in a repo secret — is a
# credential that never expires, that every workflow in the repo can read,
# and that is exfiltrated by a single malicious dependency in any job. This
# replaces it: Actions presents the OIDC token GitHub already mints for the
# run, GCP exchanges it for a token that is good for an hour, and there is no
# long-lived secret anywhere to steal.
#
# What makes it safe is not the exchange, it is what GCP will exchange FOR.
# Two independent conditions, and both are needed:
#
#   attribute_condition       which tokens the pool will accept at all — only
#                             ones GitHub minted for THIS repository.
#   workloadIdentityUser      which principals may impersonate the deployer —
#                             only that same repository's principalSet.
#
# Without the attribute_condition, any GitHub repository on the internet
# could present a token to this pool. That is the well-known way to configure
# this wrong, and GCP now rejects a provider that has no condition on a
# public issuer — but it accepts a condition that is merely weak, so the
# check to make on review is that the condition names the repository.

resource "google_iam_workload_identity_pool" "github" {
  project                   = var.project_id
  workload_identity_pool_id = "${var.service_name}-github"
  display_name              = "expense-tracker GitHub"
  description               = "Federated identities for GitHub Actions runs of ${var.github_repository}."

  depends_on = [google_project_service.this]
}

resource "google_iam_workload_identity_pool_provider" "github" {
  project                            = var.project_id
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github-actions"
  display_name                       = "GitHub Actions"

  # Only tokens GitHub issued for this repository are accepted, whatever else
  # they claim.
  #
  # jsonencode, not a hand-quoted interpolation: this string is CEL that GCP
  # evaluates, and quoting a value into a language by hand is how injection
  # happens. The variable is validated to owner/name form so there is nothing
  # to inject today — but the trust boundary of the entire keyless deploy
  # should not rest on a pair of apostrophes staying ahead of its input.
  attribute_condition = "assertion.repository == ${jsonencode(var.github_repository)}"

  # google.subject is what shows up in the audit log, so it is the token's
  # own subject — "repo:owner/name:ref:refs/heads/main" — and an audit entry
  # names the branch that deployed. The rest are what conditions and
  # principalSets can be written against later (a branch-scoped binding, say)
  # without having to touch the provider.
  attribute_mapping = {
    "google.subject"       = "assertion.sub"
    "attribute.repository" = "assertion.repository"
    "attribute.ref"        = "assertion.ref"
  }

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

# Any workflow in the repository may impersonate the deployer.
#
# Not any workflow on any branch of any fork: a fork's token carries the
# fork's own `repository` claim, so it fails the condition above and the
# exchange never happens. Tightening this further — to pushes of main only —
# is one attribute away:
#
#   .../attribute.ref/refs/heads/main   (and add ref to the mapping's uses)
#
# It is left at repository scope because a PR workflow that plans (rather
# than applies) is a natural thing to want, and it would need exactly this.
resource "google_service_account_iam_member" "deployer_wif" {
  service_account_id = google_service_account.deployer.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository/${var.github_repository}"
}
