package provider

import (
	"context"
	"os"
	"testing"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestLiveInventoryReadOnly(t *testing.T) {
	if os.Getenv("SYNOLOGY_LIVE_READ") != "1" {
		t.Skip("explicit read-only live test not requested")
	}
	c, err := dsm.New(os.Getenv("SYNOLOGY_ENDPOINT"), os.Getenv("SYNOLOGY_USERNAME"), os.Getenv("SYNOLOGY_PASSWORD"), "", os.Getenv("SYNOLOGY_CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	ddns, err := readDDNSRecords(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, d := types.MapValueFrom(ctx, ddnsRecordType, ddns); d.HasError() {
		t.Fatal(d)
	}
	if _, err := readDDNSProviders(ctx, c); err != nil {
		t.Fatal(err)
	}
	packages, err := readInstalledPackages(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, d := types.MapValueFrom(ctx, installedPackageType, packages); d.HasError() {
		t.Fatal(d)
	}
	center := NewPackageCenterResource().(*packageCenterResource)
	center.client = c
	if _, err := center.readPackageCenter(ctx, readInstalledPackages); err != nil {
		t.Fatal(err)
	}
	interfaces, err := readPhysicalInterfaces(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, d := types.MapValue(interfaceObjectType(), interfaces); d.HasError() {
		t.Fatal(d)
	}

}
