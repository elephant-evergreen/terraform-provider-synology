package dsm

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
)

type InstalledPackages struct {
	Packages *[]json.RawMessage `json:"packages"`
	Total    *int               `json:"total"`
}

func (c *Client) ListInstalledPackages(ctx context.Context) (InstalledPackages, error) {
	const additional = `["status","ctl_uninstall","startable","is_uninstall_pages","dsm_apps","installed_info","install_type","autoupdate","silent_upgrade","beta"]`
	var result InstalledPackages
	err := c.Call(ctx, "SYNO.Core.Package", 2, "list", url.Values{"offset": {"0"}, "limit": {"-1"}, "additional": {additional}}, &result)
	return result, err
}

func (c *Client) RefreshInstalledPackages(ctx context.Context) (InstalledPackages, error) {
	c.mutation.RLock()
	defer c.mutation.RUnlock()
	for ctx.Err() == nil {
		value, err, shared := c.packages.Do("installed", func() (any, error) {
			result, err := c.ListInstalledPackages(ctx)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return result, err
		})
		if ctx.Err() != nil {
			break
		}
		if shared && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			continue
		}
		if err != nil {
			return InstalledPackages{}, err
		}
		return value.(InstalledPackages), nil
	}
	return InstalledPackages{}, ctx.Err()
}
