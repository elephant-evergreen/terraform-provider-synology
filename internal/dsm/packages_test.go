package dsm

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

type packageTransport func(*http.Request) (*http.Response, error)

func (f packageTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestPackageRefreshSharesOnlyInFlightInventory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finish := make(chan struct{})
		calls := 0
		client := Client{
			endpoint: "https://dsm.example.test",
			apis: map[string]Capability{
				"SYNO.Core.Package": {Path: "entry.cgi", Min: 2, Max: 2},
			},
			http: &http.Client{Transport: packageTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if err := req.ParseForm(); err != nil || req.Form.Get("method") != "list" {
					t.Errorf("Expected a package list request, got %v", err)
				}
				body := `{"success":true,"data":{"packages":[{"id":"observed"}],"total":1}}`
				if calls == 1 {
					<-finish
				} else {
					body = `{"success":true,"data":{"packages":[],"total":0}}`
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})},
		}
		var reads sync.WaitGroup
		for range 8 {
			reads.Go(func() {
				result, err := client.RefreshInstalledPackages(t.Context())
				if err != nil || result.Total == nil || *result.Total != 1 {
					t.Errorf("Concurrent refresh = %+v, %v", result, err)
				}
			})
		}
		synctest.Wait()
		close(finish)
		reads.Wait()
		if calls != 1 {
			t.Fatalf("Shared refresh sent %d requests, want 1", calls)
		}
		err := client.WithMutation(func() error {
			result, err := client.ListInstalledPackages(t.Context())
			if err != nil || result.Total == nil || *result.Total != 0 {
				t.Errorf("Pre-write inventory = %+v, %v", result, err)
			}
			return err
		})
		if err != nil || calls != 2 {
			t.Fatalf("Pre-write inventory after %d requests = %v", calls, err)
		}
		if _, err := client.RefreshInstalledPackages(t.Context()); err != nil || calls != 3 {
			t.Fatalf("Refresh after the mutation lock sent %d requests, error %v", calls, err)
		}
	})
}
