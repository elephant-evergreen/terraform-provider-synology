package main

import (
	"context"
	"flag"
	"log"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "Enable Terraform debugger support")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/elephant-evergreen/synology",
		Debug:   *debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
