resource "synology_share_permissions" "archive" {
  share = synology_shared_folder.archive.name
  users = {}
  groups = {
    administrators             = "read_write"
    (synology_group.team.name) = "read_only"
  }
}
