variable "ddns_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "synology_ddns_record" "nas" {
  provider_id         = "ExampleDNS"
  hostname            = "nas.example.invalid"
  username            = "example-account"
  password_wo         = var.ddns_password
  password_version    = 1
  deletion_protection = true
}
