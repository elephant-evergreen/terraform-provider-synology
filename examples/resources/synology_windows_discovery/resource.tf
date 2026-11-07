resource "synology_windows_discovery" "main" {
  enabled = synology_smb.main.enabled
}
