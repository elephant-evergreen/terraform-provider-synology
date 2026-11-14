resource "synology_network_interface" "lan" {
  name      = "eth0"
  ipv4_dhcp = true
  ipv6_type = "auto"
  mtu       = 1500
}
