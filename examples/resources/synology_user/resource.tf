resource "synology_user" "reader" {
  name                = "reader"
  description         = "Archive reader"
  groups              = ["users", synology_group.team.name]
  deletion_protection = true
}
