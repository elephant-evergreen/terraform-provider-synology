resource "synology_package_center" "nas" {
  enabled             = false
  beta_channel        = false
  dsm_notifications   = true
  email_notifications = false
}
