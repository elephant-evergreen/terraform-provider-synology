resource "synology_time_machine" "nas" {
  smb_enabled = true
  afp_enabled = false
  shares      = [synology_shared_folder.backups.name]
}
import {
  to = synology_time_machine.nas
  id = "time_machine"
}
