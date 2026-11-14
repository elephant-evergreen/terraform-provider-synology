terraform {
  required_version = ">= 1.17.1"

  required_providers {
    synology = {
      source  = "elephant-evergreen/synology"
      version = "~> 0.4.2"
    }
  }
}

provider "synology" {}

data "synology_network_interfaces" "nas" {}

output "interfaces" {
  value = sort(keys(data.synology_network_interfaces.nas.interfaces))
}
