resource "synology_quickconnect_upnp" "nas" {
  enabled = false
}
import {
  to = synology_quickconnect_upnp.nas
  id = "quickconnect_upnp"
}
