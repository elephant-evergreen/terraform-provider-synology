resource "synology_package" "application" {
  package_id          = "ExamplePackage"
  running             = true
  deletion_protection = true
}
import {
  to = synology_package.application
  id = "ExamplePackage"
}
