resource "synology_quickconnect" "nas" {
  enabled       = true
  identifier    = "example-nas"
  relay_enabled = true
  services = {
    dsm_portal   = true
    mobile_apps  = true
    file_sharing = false
  }
}
import {
  to = synology_quickconnect.nas
  id = "quickconnect"
}
