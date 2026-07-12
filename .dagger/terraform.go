package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dagger/expense-tracker/internal/dagger"
)

// Terraform runs from a container, never from a desk.
//
// The same argument the rest of this module makes about the build applies to
// the infrastructure: what CI runs and what you run have to be one thing, or
// they drift. A `terraform` on someone's PATH is a version, a plugin cache,
// and a set of ambient credentials that nobody else has — and it applies to
// production. So there is no Terraform on the host here; the CLI is pinned
// to an image, the credentials are an explicit Secret argument, and the
// commands are Dagger functions:
//
//	dagger call terraform validate                     # fmt + validate; no cloud, no creds
//	dagger call terraform --project=… … state-bucket   # the GCS bucket the state lives in
//	dagger call terraform --project=… … plan
//	dagger call terraform --project=… … apply
//	dagger call terraform --project=… … output         # JSON; the deploy story's inputs
//
// Credentials are a short-lived access token, passed as a Dagger Secret and
// never written to a file:
//
//	--access-token=cmd://"gcloud auth print-access-token"
//
// which is a token that expires within the hour, not a service-account key —
// the same posture the deploy pipeline takes with Workload Identity
// Federation, so neither this repo nor CI ever holds a static cloud key.

const (
	// terraformImage pins the CLI. Bump it deliberately: a Terraform minor
	// upgrades the state file on first write, and the state is shared.
	terraformImage = "hashicorp/terraform:1.15"

	// gcloudImage is only used to create the state bucket, which Terraform
	// cannot create for itself (see stateBucket).
	gcloudImage = "gcr.io/google.com/cloudsdktool/google-cloud-cli:stable"

	// tfDir is where the root module is mounted in the container.
	tfDir = "/deploy"
)

// Terraform is the deploy/ root module together with everything it needs to
// talk to a project. Constructed by ExpenseTracker.Terraform; every command
// is a method on it, so `--project` and friends are given once:
//
//	dagger call terraform --project=p --owner-email=o --access-token=… plan
type Terraform struct {
	// Source is deploy/ — the root module, and nothing else in the repo.
	Source *dagger.Directory

	// Project is the Google Cloud project every resource is created in.
	Project string

	// Region carries Cloud Run, Artifact Registry, and the state bucket.
	Region string

	// OwnerEmail is the app's allowlist and, until the service goes public,
	// the one principal that may invoke it.
	OwnerEmail string

	// Bucket holds the remote state. Empty means "<project>-tfstate". It is
	// not called StateBucket because the function that creates it is, and a
	// Dagger object cannot expose a field and a method under one name.
	Bucket string

	// AllowUnauthenticated opens the service to allUsers. Off until the app
	// authenticates its own callers — see deploy/variables.tf.
	AllowUnauthenticated bool

	// AccessToken is a short-lived Google OAuth token. Both the provider and
	// the GCS backend read it from GOOGLE_OAUTH_ACCESS_TOKEN, and gcloud from
	// CLOUDSDK_AUTH_ACCESS_TOKEN, so one secret authenticates everything here.
	AccessToken *dagger.Secret
}

// Terraform builds the root module's command surface. Only `validate` works
// with no arguments — it is the one command that touches no cloud, which is
// exactly why CI can run it on every pull request.
func (m *ExpenseTracker) Terraform(
	// The root module. Only deploy/ is uploaded — the Go sources have no
	// bearing on the infrastructure, and a source change that could not
	// possibly alter a plan should not invalidate one.
	//
	// +defaultPath="/deploy"
	// +ignore=[".terraform", "*.tfstate", "*.tfstate.*", "*.tfvars", "*.tfvars.json"]
	source *dagger.Directory,
	// Google Cloud project id. Required by everything except `validate`.
	//
	// +optional
	project string,
	// +optional
	// +default="us-central1"
	region string,
	// The owner's Google account. Required by `plan` and `apply`.
	//
	// +optional
	ownerEmail string,
	// GCS bucket for the remote state. Defaults to "<project>-tfstate".
	//
	// +optional
	stateBucket string,
	// Grant roles/run.invoker to allUsers. Leave it off until the app checks
	// who is calling.
	//
	// +optional
	allowUnauthenticated bool,
	// A short-lived Google OAuth access token:
	//
	//	--access-token=cmd://"gcloud auth print-access-token"
	//
	// +optional
	accessToken *dagger.Secret,
) *Terraform {
	return &Terraform{
		Source:               source,
		Project:              project,
		Region:               region,
		OwnerEmail:           ownerEmail,
		Bucket:               stateBucket,
		AllowUnauthenticated: allowUnauthenticated,
		AccessToken:          accessToken,
	}
}

// Validate is the pull-request gate: the module is formatted, its syntax and
// its references hold, and every provider it names can be resolved.
//
// It initializes with `-backend=false`, so it needs no bucket, no project,
// and no credentials — which is the only reason CI can run it at all. A
// validate that required cloud access would either not run on pull requests
// or would hand every fork's pull request a token.
//
// What it therefore cannot catch is anything only the API knows: a quota, a
// name already taken, an invalid region. That is what `plan` is for.
func (t *Terraform) Validate(ctx context.Context) (string, error) {
	return t.base().
		// Formatting first: `fmt -check` exits non-zero on a file that is not
		// canonically formatted, and -diff prints it, so the failure names the
		// file and the lines rather than saying "no".
		WithExec([]string{"terraform", "fmt", "-check", "-recursive", "-diff"}).
		WithExec([]string{"terraform", "init", "-backend=false"}).
		WithExec([]string{"terraform", "validate"}).
		Stdout(ctx)
}

// StateBucket creates the GCS bucket the remote state lives in, and is
// idempotent — run it once per project, or every time, it makes no
// difference.
//
// It exists because of a genuine ordering problem, not for want of a
// resource: Terraform initializes its backend before it evaluates any
// configuration, so a bucket declared in this root module could never be the
// bucket this root module stores its state in. Somebody has to create it
// out of band. The usual "somebody" is a human following a README, which is
// how state buckets end up with no versioning; this is that step, written
// down and executable.
//
// Versioning is the point of the exercise. A corrupted or truncated state
// file is recoverable from an earlier generation; without it, the only record
// of what Terraform believes it created is gone, and every resource has to be
// imported by hand.
//
// +cache="never"
func (t *Terraform) StateBucket(ctx context.Context) (string, error) {
	if err := t.requireCloud(); err != nil {
		return "", err
	}

	return dag.Container().
		From(gcloudImage).
		WithSecretVariable("CLOUDSDK_AUTH_ACCESS_TOKEN", t.AccessToken).
		WithEnvVariable("CLOUDSDK_CORE_PROJECT", t.Project).
		WithEnvVariable("BUCKET", t.bucket()).
		WithEnvVariable("LOCATION", t.Region).
		WithEnvVariable(nonceVar, nonce()).
		WithExec([]string{"sh", "-c", createStateBucket}).
		Stdout(ctx)
}

// createStateBucket is written to be safe to re-run: it creates the bucket
// only if it is not there, then asserts the properties it must have either
// way, so a bucket that predates this function (or that someone turned
// versioning off on) is corrected rather than left alone.
const createStateBucket = `set -eu
if gcloud storage buckets describe "gs://${BUCKET}" >/dev/null 2>&1; then
	echo "state bucket gs://${BUCKET} already exists"
else
	gcloud storage buckets create "gs://${BUCKET}" \
		--location="${LOCATION}" \
		--uniform-bucket-level-access \
		--public-access-prevention
	echo "created state bucket gs://${BUCKET} in ${LOCATION}"
fi

# Terraform state names every resource in the project, and the bucket holding
# it should be no more reachable than the project itself.
gcloud storage buckets update "gs://${BUCKET}" --versioning
echo "versioning is on for gs://${BUCKET}"
`

// Plan reports what an apply would change, and changes nothing. Run it
// first — always, but especially on this module: an apply that recreates the
// Firestore database is a plan you want to have read.
//
// +cache="never"
func (t *Terraform) Plan(ctx context.Context) (string, error) {
	c, err := t.init()
	if err != nil {
		return "", err
	}
	return c.WithExec([]string{"terraform", "plan", "-input=false", "-lock-timeout=60s"}).Stdout(ctx)
}

// Apply makes it so.
//
// -auto-approve, because there is no terminal here to approve at: a Dagger
// function's exec has no stdin. `plan` is the review step, and it is not
// optional in the way an interactive prompt lets you pretend it is — read it.
//
// +cache="never"
func (t *Terraform) Apply(ctx context.Context) (string, error) {
	c, err := t.init()
	if err != nil {
		return "", err
	}
	return c.WithExec([]string{"terraform", "apply", "-input=false", "-auto-approve", "-lock-timeout=60s"}).Stdout(ctx)
}

// Output returns the root module's outputs as JSON — the registry to publish
// to, the service to deploy, the identities to impersonate. It is how the
// deploy story (#7) learns what this story created, instead of repeating the
// names in a workflow file where they can go stale.
//
// +cache="never"
func (t *Terraform) Output(ctx context.Context) (string, error) {
	c, err := t.init()
	if err != nil {
		return "", err
	}
	return c.WithExec([]string{"terraform", "output", "-json"}).Stdout(ctx)
}

// base is the module in a container, with no credentials and no backend: the
// most that can be done to it offline.
func (t *Terraform) base() *dagger.Container {
	return dag.Container().
		From(terraformImage).
		// The providers are ~150MB and identical between runs. Cached, an
		// init is a copy; uncached, CI re-downloads them on every leg.
		WithMountedCache("/plugins", dag.CacheVolume("expense-tracker-tf-plugins")).
		WithEnvVariable("TF_PLUGIN_CACHE_DIR", "/plugins").
		// No terminal: never prompt, never assume one is watching.
		WithEnvVariable("TF_IN_AUTOMATION", "1").
		WithEnvVariable("TF_INPUT", "0").
		WithMountedDirectory(tfDir, t.Source).
		WithWorkdir(tfDir)
}

// init is base plus credentials, the variables, and a backend pointed at the
// state bucket. Everything that touches the cloud starts here.
func (t *Terraform) init() (*dagger.Container, error) {
	if err := t.requireCloud(); err != nil {
		return nil, err
	}
	if t.OwnerEmail == "" {
		return nil, fmt.Errorf("--owner-email is required: it is the app's allowlist and, until the service goes public, the only principal allowed to invoke it")
	}

	return t.base().
		// Read by both the google provider and the gcs backend. A Secret, so
		// it is mounted for the exec rather than baked into an image layer,
		// and it is a token that expires — not a key that does not.
		WithSecretVariable("GOOGLE_OAUTH_ACCESS_TOKEN", t.AccessToken).
		WithEnvVariable("TF_VAR_project_id", t.Project).
		WithEnvVariable("TF_VAR_region", t.Region).
		WithEnvVariable("TF_VAR_owner_email", t.OwnerEmail).
		WithEnvVariable("TF_VAR_allow_unauthenticated", strconv.FormatBool(t.AllowUnauthenticated)).
		// The bucket cannot live in the backend block — it does not exist when
		// the config is written, and the config does not know the project. See
		// deploy/versions.tf.
		WithExec([]string{"terraform", "init", "-input=false", "-backend-config=bucket=" + t.bucket()}).
		// Everything downstream of this reads state that lives in GCS and
		// describes a cloud, neither of which Dagger can see change. Without
		// this, a second `plan` with the same arguments replays the first
		// one's output — a plan that was true when it was cached, presented as
		// if it were true now. See nonce.
		WithEnvVariable(nonceVar, nonce()), nil
}

// bucket is the state bucket's name: "<project>-tfstate" unless told
// otherwise. Deriving it from the project makes the common case
// argument-free, and project ids are globally unique, so the derived name
// generally is too.
func (t *Terraform) bucket() string {
	if t.Bucket != "" {
		return t.Bucket
	}
	return t.Project + "-tfstate"
}

// requireCloud rejects the calls that cannot work rather than letting
// Terraform fail somewhere further in, with a message about an empty project
// id that reads like a bug in the module.
func (t *Terraform) requireCloud() error {
	var missing []string
	if t.Project == "" {
		missing = append(missing, "--project")
	}
	if t.AccessToken == nil {
		missing = append(missing, `--access-token (e.g. --access-token=cmd://"gcloud auth print-access-token")`)
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("this command talks to Google Cloud and needs %s; `validate` is the one that does not",
		strings.Join(missing, " and "))
}

// nonceVar is inert — nothing reads it. Its only job is to be different on
// every call, so the exec that follows it in the chain has a cache key that
// has never been seen.
//
// Dagger caches an exec by the digest of the container it runs in, which is
// the right thing to do for a build and the wrong thing for a command whose
// answer depends on a remote API and a state file in a bucket. `+cache=never`
// on the function is only half of the fix: it makes the body run again, but
// the body then reconstructs the identical container and the exec inside it
// hits the cache anyway. This is the other half.
const nonceVar = "TF_DAGGER_INVOCATION"

func nonce() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
