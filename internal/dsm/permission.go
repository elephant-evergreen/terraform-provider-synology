package dsm

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
)

type Permission struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"is_readonly"`
	Writable bool   `json:"is_writable"`
	Deny     bool   `json:"is_deny"`
}

func (p Permission) Access() (string, error) {
	flags := 0
	for _, v := range []bool{p.ReadOnly, p.Writable, p.Deny} {
		if v {
			flags++
		}
	}
	if flags > 1 {
		return "", errors.New("DSM returned conflicting explicit permission flags")
	}
	if p.Deny {
		return "deny", nil
	}
	if p.Writable {
		return "read_write", nil
	}
	if p.ReadOnly {
		return "read_only", nil
	}
	return "inherit", nil
}

func (c *Client) permissions(ctx context.Context, share, kind string) ([]map[string]json.RawMessage, error) {
	if kind != "local_user" && kind != "local_group" {
		return nil, errors.New("principal_type must be local_user or local_group")
	}
	var result struct {
		Items []map[string]json.RawMessage `json:"items"`
		Total *int                         `json:"total"`
	}
	err := c.Call(ctx, "SYNO.Core.Share.Permission", 1, "list", url.Values{"name": {share}, "offset": {"0"}, "limit": {"-1"}, "action": {"enum"}, "user_group_type": {kind}}, &result)
	if err != nil {
		return nil, err
	}
	if result.Items == nil || (result.Total != nil && *result.Total != len(result.Items)) {
		return nil, errors.New("DSM permission listing is incomplete")
	}
	return result.Items, nil
}

func decodePermission(raw map[string]json.RawMessage) (Permission, error) {
	var p Permission
	if json.Unmarshal(raw["name"], &p.Name) != nil || p.Name == "" {
		return p, errors.New("DSM permission entry has no principal")
	}
	for name, target := range map[string]*bool{"is_readonly": &p.ReadOnly, "is_writable": &p.Writable, "is_deny": &p.Deny} {
		if json.Unmarshal(raw[name], target) != nil {
			return p, errors.New("DSM permission entry is missing an explicit access flag")
		}
	}
	_, err := p.Access()
	return p, err
}

func (c *Client) SharePermission(ctx context.Context, share, kind, principal string) (Permission, error) {
	items, err := c.permissions(ctx, share, kind)
	if err != nil {
		return Permission{}, err
	}
	for _, item := range items {
		p, err := decodePermission(item)
		if err != nil {
			return Permission{}, err
		}
		if p.Name == principal {
			return p, nil
		}
	}
	return Permission{}, ErrNotFound
}

func (c *Client) SetSharePermission(ctx context.Context, share, kind, principal, access string) error {
	if access != "inherit" && access != "deny" && access != "read_only" && access != "read_write" {
		return errors.New("invalid share access level")
	}
	if (kind == "local_group" && principal == "administrators" || kind == "local_user" && c.IsSessionUser(principal)) && access != "read_write" {
		return errors.New("refusing to revoke direct management access to a shared folder")
	}
	c.mutation.Lock()
	defer c.mutation.Unlock()
	items, err := c.permissions(ctx, share, kind)
	if err != nil {
		return err
	}
	found := false
	for _, item := range items {
		p, err := decodePermission(item)
		if err != nil {
			return err
		}
		if p.Name != principal {
			continue
		}
		if found {
			return errors.New("DSM returned duplicate permission identities")
		}
		found = true
		item["is_readonly"] = json.RawMessage(JSON(access == "read_only"))
		item["is_writable"] = json.RawMessage(JSON(access == "read_write"))
		item["is_deny"] = json.RawMessage(JSON(access == "deny"))
	}
	if !found {
		return errors.New("permission principal was not found in DSM; create or import its account or group first")
	}
	return c.Call(ctx, "SYNO.Core.Share.Permission", 1, "set", url.Values{"name": {share}, "user_group_type": {kind}, "permissions": {JSON(items)}}, nil)
}
