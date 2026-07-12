# The event log.
#
# Native mode, not Datastore mode: the app's client library, the emulator it
# is developed against, and the `events` collection all assume it.
#
# This is the one resource in this module that is not rebuildable. Cloud Run,
# the registry, the service accounts — destroy them and a re-apply plus a
# re-deploy puts them back. Destroy this and the expense history is gone;
# it is an append-only log, and the log IS the product. So it is defended
# twice over, both flags load-bearing:
#
#   delete_protection_state  Google itself refuses to delete the database
#                            until someone flips this off in a separate,
#                            deliberate apply.
#   deletion_policy          `terraform destroy` drops it from state rather
#                            than trying to delete it at all.
#
# Together: no single command, run by mistake, takes the log with it.
resource "google_firestore_database" "events" {
  project     = var.project_id
  name        = "(default)"
  location_id = var.firestore_location
  type        = "FIRESTORE_NATIVE"

  delete_protection_state = "DELETE_PROTECTION_ENABLED"
  deletion_policy         = "ABANDON"

  depends_on = [google_project_service.this]
}
