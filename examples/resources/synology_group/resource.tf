resource "synology_group" "team" {
  name                = "team"
  description         = "Archive readers"
  deletion_protection = true
}
