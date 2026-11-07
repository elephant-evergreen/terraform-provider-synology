resource "synology_time" "main" {
  synchronization = "ntp"
  ntp_server      = "time.example.org"
  timezone        = "Seoul"
}
