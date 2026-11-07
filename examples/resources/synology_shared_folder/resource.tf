resource "synology_shared_folder" "archive" {
  name                   = "archive"
  volume                 = "/volume1"
  description            = "Shared archive"
  recycle_bin            = true
  recycle_bin_admin_only = true
  deletion_protection    = true

  lifecycle {
    prevent_destroy = true
  }
}
