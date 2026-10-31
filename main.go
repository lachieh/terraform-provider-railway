// Command terraform-provider-railway serves the railway provider
// over the Terraform plugin protocol.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/lachieh/terraform-provider-railway/internal/provider"
)

//go:generate go tool tfplugindocs generate --provider-name railway --rendered-provider-name Railway --tf-version 1.17.0

var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/lachieh/railway",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
