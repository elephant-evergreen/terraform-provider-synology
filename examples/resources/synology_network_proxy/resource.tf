resource "synology_network_proxy" "nas" {
  enabled = false
}
import {
  to = synology_network_proxy.nas
  id = "network_proxy"
}
