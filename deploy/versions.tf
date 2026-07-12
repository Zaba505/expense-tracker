# Pins and remote state.
#
# Terraform is never run from a desk here: every command goes through the
# Dagger module (`dagger call terraform ...`), which pins the CLI image. The
# constraint below is therefore a floor, not the version anyone actually
# runs — that one lives in .dagger/terraform.go.

terraform {
  required_version = ">= 1.13.0, < 2.0.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
  }

  # Remote state in GCS, so the state is not on whoever's laptop applied
  # last. The bucket is deliberately NOT named here: it has to exist before
  # Terraform can initialize against it, so nothing in this root module can
  # create it. `dagger call terraform state-bucket` does (idempotently), and
  # `init` is passed `-backend-config=bucket=...`.
  #
  # That is the whole of the chicken-and-egg: one bucket, created out of
  # band, versioned so a corrupted state can be rolled back.
  backend "gcs" {
    prefix = "expense-tracker"
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}
