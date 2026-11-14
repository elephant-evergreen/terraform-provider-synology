resource "synology_user" "reader" {
  name                = "reader"
  description         = "Archive reader"
  groups              = ["users", "team"]
  deletion_protection = true
}
