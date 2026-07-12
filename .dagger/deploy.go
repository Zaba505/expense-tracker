package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/expense-tracker/internal/dagger"
)

// The deploy: publish the image, then point Cloud Run at it.
//
// It is one function and not two on purpose. The tag the z5labs pipeline
// publishes is derived from the commit — <shortSha>-<commitISO> — so a
// deploy that took a tag as an argument would be a deploy you could point at
// anything, including an image nobody built from this tree. Here the only
// thing that can be rolled out is what this very call just published, and the
// only commits that publish at all are the ones on main (see publishOn).
//
// What it deliberately does NOT do is configure the service. Terraform owns
// the Cloud Run service — its identity, its env, its probes, its scaling,
// min-instances 0 — and this changes the image and nothing else; run.tf's
// ignore_changes is the other half of that bargain. So the service has to
// exist first, and this refuses to run if it does not, rather than letting
// `gcloud run deploy` create a service with none of that configuration on it.
//
//	dagger call deploy --project=<id> \
//	  --access-token=cmd://"gcloud auth print-access-token"
//
// The same command CI runs (.github/workflows/ci.yml), with the same kind of
// credential: a token that expires within the hour. In CI it comes from
// Workload Identity Federation instead of from gcloud, which is the only
// difference — there is no service-account key in this repo, in GitHub, or on
// a desk.

// Deploy publishes this commit's images through the z5labs pipeline and rolls
// the server image out to Cloud Run. It returns the service's URL.
//
// One token does both halves: Artifact Registry takes it as the password for
// the oauth2accesstoken user, and gcloud reads it from the environment.
//
// Not cached, and the exec inside carries a nonce for the same reason
// terraform.go's do: what a deploy changes lives in Google's API, which
// Dagger cannot see, so an identical second call must actually run again
// rather than replay the first one's output.
//
// +cache="never"
func (m *ExpenseTracker) Deploy(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
	// Google Cloud project that owns the registry and the service.
	project string,
	// A short-lived Google OAuth access token — it authenticates both the
	// registry push and gcloud:
	//
	//	--access-token=cmd://"gcloud auth print-access-token"
	accessToken *dagger.Secret,
	// Region of the Cloud Run service and the registry. Must match the
	// region deploy/ was applied with.
	//
	// +optional
	// +default="us-central1"
	region string,
	// Cloud Run service to roll the image out to; deploy/variables.tf's
	// service_name.
	//
	// +optional
	// +default="expense-tracker"
	service string,
	// Artifact Registry repository the images are published to;
	// deploy/variables.tf's artifact_registry_repository.
	//
	// +optional
	// +default="containers"
	artifactRegistryRepository string,
) (string, error) {
	// The same string deploy/outputs.tf's `registry` output produces. It is
	// Google's own URL scheme rather than a name this repo invents, so
	// deriving it here costs nothing and saves the workflow a variable to
	// hold — and to let go stale.
	registry := fmt.Sprintf("%s-docker.pkg.dev/%s/%s", region, project, artifactRegistryRepository)

	// The z5labs pipeline, not a build of our own: it checks, builds both
	// binaries multi-arch, and publishes them — but only for a ref matching
	// publishOn, so this is a no-op push on any branch but main. The importer
	// rides along; it is this commit's artifact too, and the import story
	// will want it in the registry.
	if err := m.Ci(ctx, source, registry, accessToken); err != nil {
		return "", fmt.Errorf("publish: %w", err)
	}

	tag, err := m.ImageTag(ctx, source)
	if err != nil {
		return "", err
	}
	image := fmt.Sprintf("%s/%s:%s", registry, binServer, tag)

	return dag.Container().
		From(gcloudImage).
		WithSecretVariable("CLOUDSDK_AUTH_ACCESS_TOKEN", accessToken).
		WithEnvVariable("CLOUDSDK_CORE_PROJECT", project).
		WithEnvVariable("SERVICE", service).
		WithEnvVariable("REGION", region).
		WithEnvVariable("IMAGE", image).
		WithEnvVariable(nonceVar, nonce()).
		WithExec([]string{"sh", "-c", runDeploy}).
		Stdout(ctx)
}

// runDeploy checks its two assumptions before it changes anything, because
// both of them fail in ways that are otherwise hard to read. Only stdout is
// swallowed, never stderr: gcloud says exactly what went wrong — no
// permission, no such service, bad token — and a check that hid that behind
// its own guess would be worse than no check at all. The message that follows
// says what the failure means for the deploy, not what it was.
//
// The rollout itself is the test of the deployed app. `gcloud run deploy`
// does not return until the new revision passes its startup probe and takes
// traffic — and that probe is GET /health/readiness (deploy/run.tf), which
// round-trips a document through Firestore. So a deploy that returns is a
// deployed service that has already written to and read from the event log
// with the runtime account's own credentials. A revision that cannot reach
// Firestore never takes traffic, and the previous one keeps serving it.
const runDeploy = `set -eu

if ! gcloud run services describe "$SERVICE" --region="$REGION" >/dev/null; then
	echo >&2
	echo "could not read a Cloud Run service named '$SERVICE' in $REGION (gcloud's error is above)." >&2
	echo "If it does not exist yet, that is deliberate and not something to fix here: the service and all of its configuration — its identity, env, probes, scaling — belong to Terraform (deploy/run.tf), and this deploy only changes the image it runs. A service created here would have none of it. Stand the environment up first: dagger call terraform --project=$CLOUDSDK_CORE_PROJECT ... apply" >&2
	exit 1
fi

if ! gcloud artifacts docker images describe "$IMAGE" >/dev/null; then
	echo >&2
	echo "the pipeline published, but $IMAGE could not be read back (gcloud's error is above)." >&2
	echo "If nothing is tagged that, then the tag is no longer derived the same way twice — once by the z5labs pipeline when it pushes, once by this module to know what to deploy (see ImageTag). Deploying is not safe here, so nothing is deployed: whatever is running now keeps running." >&2
	exit 1
fi

gcloud run deploy "$SERVICE" --image="$IMAGE" --region="$REGION" --quiet
gcloud run services describe "$SERVICE" --region="$REGION" --format='value(status.url)'
`

// ImageTag is the tag this commit's images are published under —
// "<shortSha>-<commitISO>", the z5labs pipeline's scheme for a branch build:
//
//	dagger call image-tag
//	# 4f2a1c9-2026-07-12T14-03-22-05-00
//
// The pipeline does not tell anyone what it pushed, so the deploy has to
// derive it. That is a duplicated assumption, and the honest thing to do with
// one is to state it and then check it: Deploy asks the registry whether that
// exact tag is really there before it rolls anything out, so a change to
// z5labs' scheme fails the deploy with a message that says so, rather than
// deploying a stale image that happens to still exist.
//
// It reads git, so — like `ci` — it needs a real .git directory and does not
// work from inside a git worktree.
func (m *ExpenseTracker) ImageTag(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
) (string, error) {
	git := dag.Container().
		From(goImage).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src")

	sha, err := git.WithExec([]string{"git", "rev-parse", "--short", "HEAD"}).Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("git rev-parse --short HEAD: %w", err)
	}

	// The committer date, not the author date: it is the one that changes on
	// a rebase, and so the one that distinguishes two builds of what is
	// otherwise the same patch.
	iso, err := git.WithExec([]string{"git", "show", "-s", "--format=%cI", "HEAD"}).Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("git show commit time: %w", err)
	}

	return strings.TrimSpace(sha) + "-" + sanitizeDockerTag(strings.TrimSpace(iso)), nil
}

// sanitizeDockerTag maps a string into Docker's tag charset [A-Za-z0-9_.-],
// replacing anything else with '-'. On an ISO 8601 timestamp that means the
// ':' of the time and the '+' of a positive offset, which is the whole reason
// the pipeline does it: "2026-07-12T14:03:22-05:00" is not a legal tag.
//
// z5labs' version also rewrites a leading '.' or '-' and truncates at 128
// characters. Neither can apply here: the tag it is spliced into starts with
// a hex sha and is about thirty characters long.
func sanitizeDockerTag(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}
