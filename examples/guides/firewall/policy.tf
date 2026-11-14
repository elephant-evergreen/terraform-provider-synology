resource "synology_firewall" "nas" {
  enabled             = true
  profile_name        = "custom"
  port_check_enabled  = true
  management_networks = ["192.0.2.0/24"]
  adapters = {
    global = {
      default_policy = "none"
      rules = [{
        name           = "DSM management"
        protocol       = "tcp"
        port_group     = "custom"
        ports          = "5001"
        port_direction = "destination"
        source_group   = "netmask"
        source         = "192.0.2.0/255.255.255.0"
      }]
    }
  }
}
