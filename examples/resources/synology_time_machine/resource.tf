resource "synology_time_machine" "nas" {
  smb_enabled = true
  afp_enabled = false
  shares      = ["backups"]
}
