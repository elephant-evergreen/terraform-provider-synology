resource "synology_firewall" "nas" {}

import {
  to = synology_firewall.nas
  id = "firewall"
}
