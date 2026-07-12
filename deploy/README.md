# deploy/

The cloud footprint, as code: one Terraform root module that stands up
everything the app runs on, and nothing it does not.

**Terraform is never run from your PATH.** Every command goes through the
repo's Dagger module, which pins the CLI version, mounts only this
directory, and takes credentials as an explicit short-lived token:

```sh
dagger call terraform validate                       # no cloud, no credentials — this is the CI gate
dagger call terraform --project=<id> --owner-email=<you@example.com> \
  --access-token=cmd://"gcloud auth print-access-token" plan
```

The rationale, the argument list, and the caching sharp edge are in
[`.dagger/terraform.go`](../.dagger/terraform.go).

## What it provisions

| Resource                                            | File           | Notes                                                                     |
| --------------------------------------------------- | -------------- | ------------------------------------------------------------------------- |
| Firestore, native mode                              | `firestore.tf` | the event log; delete-protected, and `destroy` abandons rather than drops |
| Artifact Registry (Docker)                          | `registry.tf`  | where the z5labs pipeline publishes; keeps the last 10, expires past 30d  |
| Cloud Run service, min-instances 0                  | `run.tf`       | probes wired to `/health/readiness` and `/health/liveness`                |
| Runtime service account + `roles/datastore.user`    | `iam.tf`       | what the app runs as; it can touch the log and nothing else               |
| Deployer service account + Workload Identity pool   | `iam.tf`, `wif.tf` | what GitHub Actions impersonates — no key, no repo secret            |
| Secret Manager secret for the Sign-In client secret | `secrets.tf`   | the container only; the *value* is added out of band (see below)          |
| The APIs all of the above need                      | `services.tf`  | everything `depends_on` this                                              |

Three of those decisions are load-bearing and easy to undo by accident, so
they are argued at length in the files themselves rather than here:
Firestore's two layers of delete protection, Cloud Run's image being owned by
`gcloud run deploy` and not by Terraform, and the WIF attribute condition
that is the whole trust boundary of the keyless deploy.

## Standing up an environment

You need a Google Cloud project with billing enabled, and `gcloud` logged in
as someone who can administer it. One-time, if the project is fresh:

```sh
gcloud services enable cloudresourcemanager.googleapis.com serviceusage.googleapis.com --project=<id>
```

Terraform enables the rest itself, but it cannot enable the API it needs in
order to enable APIs.

Then, from the repo root — `plan` and `apply` take the same arguments, so
export them once:

```sh
export TF_ARGS="--project=<id> --owner-email=<you@example.com> \
  --access-token=cmd://\"gcloud auth print-access-token\""

dagger call terraform $TF_ARGS state-bucket   # once per project; idempotent
dagger call terraform $TF_ARGS plan           # read it
dagger call terraform $TF_ARGS apply
dagger call terraform $TF_ARGS output         # JSON: the registry, the service, the identities
```

`state-bucket` is separate because it has to be: Terraform initializes its
backend *before* it evaluates any configuration, so the bucket the state
lives in can never be a resource in the module that stores state there. That
step is usually a paragraph in a README that someone follows by hand, which
is how state buckets end up without versioning — here it is a command, and it
turns versioning on every time it runs.

The remote state lands in `gs://<project>-tfstate/expense-tracker` (override
with `--state-bucket`).

## After the first apply

The environment is empty but complete: the Cloud Run service exists and runs
Google's placeholder image, because the registry that will hold *our* image
is created by the same apply that creates the service — it cannot pull a tag
that nothing has published yet. `dagger call terraform output` tells the
deploy story (#7) where to publish and what to roll out; from then on
`gcloud run deploy` owns the running image and Terraform ignores it.

Two things are deliberately left for the stories that need them:

- **The service is private.** `allow_unauthenticated` is `false`, because the
  app does not yet check who is calling — a public URL would be an
  unauthenticated handle on the event log. Reach it in the meantime with
  `gcloud run services proxy expense-tracker --region <region>`; the owner
  holds `roles/run.invoker`. The auth story (#13/#14) flips the variable,
  since a browser cannot complete an in-app Sign-In flow against a service
  that rejects it at the edge.
- **The Sign-In client secret has no value.** Terraform creates the secret and
  grants the runtime account read access, but never sees the secret itself —
  a value passed through a variable would sit in plaintext in the state file.
  Add it once, out of band:

  ```sh
  gcloud secrets versions add expense-tracker-oauth-client-secret --project=<id> --data-file=-
  ```

## What CI checks

`dagger call terraform validate` runs on every pull request: `terraform fmt
-check -recursive`, then `init -backend=false` and `validate`. It needs no
project and no credentials, which is exactly why it can run on a fork's pull
request — and also the limit of what it proves. Anything only the API knows —
a quota, a name already taken, an invalid region — shows up in `plan`, not
here.

There is no `destroy` function, on purpose. Tearing down an environment is
not a thing to make one keystroke long, and Firestore would refuse anyway.
