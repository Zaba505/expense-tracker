# deploy/

Infrastructure-as-code for the expense tracker: Terraform that
provisions Firestore, Artifact Registry, Cloud Run, the service account,
and Secret Manager. Populated by `story(iac)` and consumed by
`story(deploy)`.

The container image is produced by the z5labs Dagger `GoApp` archetype
(see the repo `README.md`), so there is no hand-written Dockerfile here.
