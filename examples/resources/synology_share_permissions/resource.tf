resource "synology_share_permissions" "archive" {
  share = "archive"
  users = {}
  groups = {
    administrators = "read_write"
    team           = "read_only"
  }
}
