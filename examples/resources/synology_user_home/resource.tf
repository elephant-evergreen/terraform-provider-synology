resource "synology_user_home" "main" {
  enabled        = false
  domain_enabled = false
  ldap_enabled   = false

  lifecycle {
    prevent_destroy = true
  }
}

import {
  to = synology_user_home.main
  id = "user_home"
}
