resource "synology_network" "nas" {
  hostname   = "nas"
  manual_dns = false
  ipv4_first = true
}
import {
  to = synology_network.nas
  id = "network"
}
