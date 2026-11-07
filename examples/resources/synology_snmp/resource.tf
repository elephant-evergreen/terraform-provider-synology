resource "synology_snmp" "main" {
  enabled         = false
  v1_v2_enabled   = false
  v3_enabled      = false
  privacy_enabled = false
}

import {
  to = synology_snmp.main
  id = "snmp"
}
