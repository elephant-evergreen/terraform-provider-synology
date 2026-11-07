terraform {
  required_version = ">= 1.17.1"

  required_providers {
    synology = {
      source = "elephant-evergreen/synology"
    }
  }
}

provider "synology" {}
