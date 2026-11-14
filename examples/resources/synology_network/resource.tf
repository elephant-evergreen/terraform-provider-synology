resource "synology_network" "nas" {
  hostname   = "nas"
  manual_dns = false
  ipv4_first = true
}
