package provider

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccServiceLifecycle(t *testing.T) {
	var mu sync.Mutex
	settings := map[string]any{"enable": false, "portnum": 22, "unmanaged_future_setting": "preserved"}
	writes := 0
	beforeImport := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		r.ParseForm()
		var data any = map[string]any{}
		switch r.Form.Get("api") {
		case "SYNO.API.Info":
			data = map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}, "SYNO.Core.FileServ.FTP.SFTP": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}}
		case "SYNO.API.Auth":
			data = map[string]any{"sid": "fixture-session"}
		default:
			switch r.Form.Get("method") {
			case "get":
				data = settings
			case "set":
				writes++
				if v := r.Form.Get("enable"); v != "" {
					settings["enable"] = v == "true"
				}
				if v := r.Form.Get("portnum"); v != "" {
					if v != "2022" {
						t.Error("unexpected port mutation")
					}
					settings["portnum"] = 2022
				}
			default:
				w.WriteHeader(400)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600)
	config := func(enabled bool) string {
		return fmt.Sprintf(`
provider "synology" {
 endpoint = %q
 ca_file = %q
 username = "fixture-user"
 password = "fixture-password"
}
resource "synology_sftp" "test" {
 enabled = %t
}
`, server.URL, ca, enabled)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: func(_ *terraform.State) error {
		mu.Lock()
		defer mu.Unlock()
		if settings["enable"] != true || settings["portnum"] != 22 || settings["unmanaged_future_setting"] != "preserved" {
			return fmt.Errorf("service destroy or update changed an unmanaged setting")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(false), Check: resource.TestCheckResourceAttr("synology_sftp.test", "port", "22")},
		{ResourceName: "synology_sftp.test", ImportState: true, ImportStateId: "sftp", ImportStateVerify: true, PreConfig: func() { mu.Lock(); beforeImport = writes; mu.Unlock() }, ImportStateCheck: func(_ []*terraform.InstanceState) error {
			mu.Lock()
			defer mu.Unlock()
			if beforeImport != writes {
				return fmt.Errorf("import mutated a service")
			}
			return nil
		}},
		{Config: config(true), Check: resource.TestCheckResourceAttr("synology_sftp.test", "enabled", "true")},
		{Config: config(true), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() { mu.Lock(); settings["enable"] = false; mu.Unlock() }},
		{Config: config(true)},
	}})
}

func TestLiveServicesReadOnly(t *testing.T) {
	if os.Getenv("SYNOLOGY_LIVE_READ") != "1" {
		t.Skip("explicit read-only live test not requested")
	}
	ctx := context.Background()
	c, err := dsm.New(os.Getenv("SYNOLOGY_ENDPOINT"), os.Getenv("SYNOLOGY_USERNAME"), os.Getenv("SYNOLOGY_PASSWORD"), "", os.Getenv("SYNOLOGY_CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	for _, newResource := range serviceResources() {
		r := newResource().(*serviceResource)
		r.client = c
		if _, err := r.read(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("Verified typed readback: %s", r.definition.name)
	}
}
