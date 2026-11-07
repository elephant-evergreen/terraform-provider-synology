resource "synology_file_transfer_logging" "main" {
  smb_enabled          = true
  afp_enabled          = false
  ftp_enabled          = false
  tftp_enabled         = false
  webdav_enabled       = false
  file_station_enabled = false
}
