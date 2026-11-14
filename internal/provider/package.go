package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
)

const packageAPI = "SYNO.Core.Package"

type installedPackage struct {
	ID               string `tfsdk:"id"`
	Name             string `tfsdk:"name"`
	Version          string `tfsdk:"version"`
	Status           string `tfsdk:"status"`
	Volume           string `tfsdk:"volume"`
	Startable        bool   `tfsdk:"startable"`
	CanUninstall     bool   `tfsdk:"can_uninstall"`
	CustomUninstall  bool   `tfsdk:"custom_uninstall"`
	AutomaticUpdates bool   `tfsdk:"automatic_updates"`
	ImportantUpdates bool   `tfsdk:"important_updates"`
	SilentUpgrade    bool   `tfsdk:"silent_upgrade"`
	Beta             bool   `tfsdk:"beta"`
	Apps             string `tfsdk:"apps"`
}

var packageVolumePattern = regexp.MustCompile(`^/volume[1-9][0-9]*$`)

func readInstalledPackages(ctx context.Context, client *dsm.Client) (map[string]installedPackage, error) {
	return decodeInstalledPackages(client.ListInstalledPackages(ctx))
}

func refreshInstalledPackages(ctx context.Context, client *dsm.Client) (map[string]installedPackage, error) {
	return decodeInstalledPackages(client.RefreshInstalledPackages(ctx))
}

func decodeInstalledPackages(data dsm.InstalledPackages, err error) (map[string]installedPackage, error) {
	if err != nil {
		return nil, err
	}
	if data.Packages == nil {
		return nil, errors.New("DSM omitted the complete installed-package list")
	}
	if data.Total != nil && *data.Total != len(*data.Packages) {
		return nil, errors.New("DSM returned an incomplete installed-package inventory")
	}
	result := map[string]installedPackage{}
	for _, value := range *data.Packages {
		var raw struct {
			ID         *string `json:"id"`
			Name       *string `json:"name"`
			Version    *string `json:"version"`
			Additional *struct {
				Status          *string `json:"status"`
				Startable       *bool   `json:"startable"`
				CanUninstall    *bool   `json:"ctl_uninstall"`
				CustomUninstall *bool   `json:"is_uninstall_pages"`
				Apps            *string `json:"dsm_apps"`
				Auto            *bool   `json:"autoupdate"`
				Important       *bool   `json:"autoupdate_important"`
				Silent          *bool   `json:"silent_upgrade"`
				Beta            *bool   `json:"beta"`
				InstalledInfo   *struct {
					Path *string `json:"path"`
				} `json:"installed_info"`
			} `json:"additional"`
		}
		if json.Unmarshal(value, &raw) != nil || raw.ID == nil || *raw.ID == "" || raw.Name == nil || raw.Version == nil || *raw.Version == "" || raw.Additional == nil {
			return nil, errors.New("DSM returned incomplete package identity or version")
		}
		a := raw.Additional
		if a.Status == nil || a.Startable == nil || a.CanUninstall == nil || a.CustomUninstall == nil || a.Apps == nil || a.Auto == nil || a.Important == nil || a.Silent == nil || a.Beta == nil || a.InstalledInfo == nil || a.InstalledInfo.Path == nil {
			return nil, errors.New("DSM omitted package lifecycle or update-policy metadata")
		}
		path := *a.InstalledInfo.Path
		volume := ""
		if strings.HasPrefix(path, "/usr/local/packages/") {
			volume = "system"
		} else {
			parts := strings.Split(path, "/")
			if len(parts) > 2 && packageVolumePattern.MatchString("/"+parts[1]) {
				volume = "/" + parts[1]
			}
		}
		if volume == "" {
			return nil, errors.New("DSM returned an unrecognized package installation location")
		}
		if _, exists := result[*raw.ID]; exists {
			return nil, errors.New("DSM returned duplicate package identities")
		}
		result[*raw.ID] = installedPackage{*raw.ID, *raw.Name, *raw.Version, *a.Status, volume, *a.Startable, *a.CanUninstall, *a.CustomUninstall, *a.Auto, *a.Important, *a.Silent, *a.Beta, *a.Apps}
	}
	return result, nil
}
func packageRunning(status string) (bool, bool) {
	switch status {
	case "running":
		return true, true
	case "stop", "stopped":
		return false, true
	}
	return false, false
}
func packageTransient(status string) bool {
	switch status {
	case "installing", "downloading", "queueing", "starting", "stopping", "upgrading", "repairing", "loading", "uninstalling":
		return true
	}
	return false
}
func packageFeasibility(ctx context.Context, client *dsm.Client, id, operation string) error {
	return client.Call(ctx, packageAPI, 1, "feasibility_check", url.Values{"type": {operation + "_check"}, "packages": {dsm.JSON([]string{id})}}, nil)
}
func packageControlQueue(ctx context.Context, client *dsm.Client, id, version, operation string) error {
	var data struct {
		Steps *[]struct {
			Package   *string `json:"pkg"`
			Operation *string `json:"operation"`
		} `json:"steps"`
	}
	request := []map[string]any{{"pkg": id, "version": version, "beta": false, "operation": operation}}
	if err := client.Call(ctx, packageAPI, 1, "get_queue", url.Values{"operations": {dsm.JSON(request)}}, &data); err != nil {
		return err
	}
	if data.Steps == nil || len(*data.Steps) != 1 {
		return errors.New("package operation would affect additional packages; manage those dependencies explicitly before applying")
	}
	step := (*data.Steps)[0]
	if step.Package == nil || *step.Package != id || step.Operation == nil || *step.Operation != operation {
		return errors.New("DSM returned an unexpected package operation queue")
	}
	return nil
}
