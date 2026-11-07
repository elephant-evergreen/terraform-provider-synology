package dsm

import (
	"bytes"
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
	if result.Items == nil || result.Total == nil || *result.Total != len(result.Items) {
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
		if bytes.Equal(bytes.TrimSpace(raw[name]), []byte("null")) || json.Unmarshal(raw[name], target) != nil {
			return p, errors.New("DSM permission entry is missing an explicit access flag")
		}
	}
	_, err := p.Access()
	return p, err
}
