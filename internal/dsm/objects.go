package dsm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"
)

type Share struct {
	Name                string `json:"name"`
	Description         string `json:"desc"`
	Volume              string `json:"vol_path"`
	UUID                string `json:"uuid"`
	Hidden              bool   `json:"hidden"`
	HideUnreadable      *bool  `json:"hide_unreadable"`
	RecycleBin          bool   `json:"enable_recycle_bin"`
	RecycleBinAdminOnly bool   `json:"recycle_bin_admin_only"`
}

type User struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Email       string   `json:"email"`
	Expired     string   `json:"expired"`
	UID         int64    `json:"uid"`
	Groups      []string `json:"groups"`
}

type Group struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	GID         int64  `json:"gid"`
}

func (c *Client) Shares(ctx context.Context) ([]Share, error) {
	var data struct {
		Shares []Share `json:"shares"`
		Total  *int    `json:"total"`
	}
	err := c.Call(ctx, "SYNO.Core.Share", 1, "list", url.Values{"shareType": {"all"}, "additional": {`["hidden","recyclebin","advance_setting"]`}}, &data)
	if err != nil {
		return nil, err
	}
	if data.Shares == nil || data.Total == nil || *data.Total != len(data.Shares) {
		return nil, errors.New("DSM shared-folder listing is incomplete")
	}
	for _, s := range data.Shares {
		if s.Name == "" {
			return nil, errors.New("DSM returned an unnamed shared folder")
		}
	}
	return data.Shares, nil
}

func (c *Client) Share(ctx context.Context, name string) (Share, error) {
	shares, err := c.Shares(ctx)
	if err != nil {
		return Share{}, err
	}
	for _, s := range shares {
		if s.Name == name {
			var raw json.RawMessage
			err = c.Call(ctx, "SYNO.Core.Share", 1, "get", url.Values{"name": {name}, "additional": {`["hidden","recyclebin","advance_setting"]`}}, &raw)
			if err != nil {
				return Share{}, err
			}
			var direct Share
			target := raw
			if json.Unmarshal(raw, &direct) != nil {
				return Share{}, errors.New("DSM returned an invalid shared folder")
			}
			if direct.Name == "" {
				var nested struct {
					Shares []json.RawMessage `json:"shares"`
				}
				if json.Unmarshal(raw, &nested) != nil || len(nested.Shares) != 1 {
					return Share{}, errors.New("DSM shared-folder lookup is ambiguous")
				}
				target = nested.Shares[0]
				if json.Unmarshal(target, &direct) != nil {
					return Share{}, errors.New("DSM returned an invalid shared folder")
				}
			}
			if direct.Name != name || direct.Volume == "" || direct.UUID == "" {
				return Share{}, errors.New("DSM shared-folder identity is inconsistent")
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(target, &fields) != nil {
				return Share{}, errors.New("DSM shared-folder metadata is malformed")
			}
			if err := requireStrings(fields, "name", "vol_path", "uuid", "desc"); err != nil {
				return Share{}, err
			}
			for _, field := range []string{"hidden", "hide_unreadable", "enable_recycle_bin", "recycle_bin_admin_only"} {
				var value bool
				if bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) || json.Unmarshal(fields[field], &value) != nil {
					return Share{}, errors.New("DSM shared-folder metadata is missing a managed boolean")
				}
			}
			return direct, nil
		}
	}
	return Share{}, ErrNotFound
}

func (c *Client) WriteShare(ctx context.Context, method string, s Share) error {
	if method != "create" && method != "set" {
		return errors.New("unsupported shared-folder mutation")
	}
	info := map[string]any{"name": s.Name, "desc": s.Description, "hidden": s.Hidden, "enable_recycle_bin": s.RecycleBin, "recycle_bin_admin_only": s.RecycleBinAdminOnly}
	if s.HideUnreadable != nil {
		info["hide_unreadable"] = *s.HideUnreadable
	}
	if method == "create" {
		info["vol_path"] = s.Volume
	}
	return c.Mutate(ctx, "SYNO.Core.Share", 1, method, url.Values{"name": {s.Name}, "shareinfo": {JSON(info)}})
}

func (c *Client) DeleteShare(ctx context.Context, name string) error {
	return c.Mutate(ctx, "SYNO.Core.Share", 1, "delete", url.Values{"name": {name}})
}

func (c *Client) Users(ctx context.Context) ([]User, error) {
	var data struct {
		Users []json.RawMessage `json:"users"`
		Total *int              `json:"total"`
	}
	err := c.Call(ctx, "SYNO.Core.User", 1, "list", url.Values{"offset": {"0"}, "limit": {"-1"}, "additional": {`["uid","description","email","expired"]`}}, &data)
	if err != nil {
		return nil, err
	}
	if data.Users == nil || data.Total == nil || *data.Total != len(data.Users) {
		return nil, errors.New("DSM user listing is incomplete")
	}
	users := make([]User, 0, len(data.Users))
	for _, raw := range data.Users {
		var u User
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &u) != nil || json.Unmarshal(raw, &fields) != nil {
			return nil, errors.New("DSM returned a malformed user")
		}
		if err := requireStrings(fields, "name", "description", "email", "expired"); err != nil {
			return nil, err
		}
		if u.Name == "" || u.UID <= 0 || u.Expired == "" {
			return nil, errors.New("DSM user listing is missing an identity or account state")
		}
		users = append(users, u)
	}
	return users, nil
}

func (c *Client) User(ctx context.Context, name string) (User, error) {
	users, err := c.Users(ctx)
	if err != nil {
		return User{}, err
	}
	for _, u := range users {
		if u.Name == name {
			groups, err := c.UserGroups(ctx, name, u.UID)
			if err != nil {
				return User{}, err
			}
			u.Groups = groups
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (c *Client) Groups(ctx context.Context) ([]Group, error) {
	var data struct {
		Groups []Group `json:"groups"`
		Total  *int    `json:"total"`
	}
	err := c.Call(ctx, "SYNO.Core.Group", 1, "list", url.Values{"offset": {"0"}, "limit": {"-1"}}, &data)
	if err != nil {
		return nil, err
	}
	if data.Groups == nil || data.Total == nil || *data.Total != len(data.Groups) {
		return nil, errors.New("DSM group listing is incomplete")
	}
	for _, g := range data.Groups {
		if g.Name == "" {
			return nil, errors.New("DSM returned an unnamed group")
		}
	}
	return data.Groups, nil
}

func (c *Client) Group(ctx context.Context, name string) (Group, error) {
	groups, err := c.Groups(ctx)
	if err != nil {
		return Group{}, err
	}
	for _, g := range groups {
		if g.Name == name {
			var result struct {
				Groups []json.RawMessage `json:"groups"`
				Errors []json.RawMessage `json:"errors"`
			}
			if err := c.Call(ctx, "SYNO.Core.Group", 1, "get", url.Values{"name": {JSON([]string{name})}}, &result); err != nil {
				return Group{}, err
			}
			if len(result.Errors) != 0 || len(result.Groups) != 1 {
				return Group{}, errors.New("DSM group lookup returned an incomplete or ambiguous identity")
			}
			var actual Group
			var fields map[string]json.RawMessage
			if json.Unmarshal(result.Groups[0], &actual) != nil || json.Unmarshal(result.Groups[0], &fields) != nil {
				return Group{}, errors.New("DSM returned a malformed group")
			}
			if err := requireStrings(fields, "name", "description"); err != nil {
				return Group{}, err
			}
			if actual.Name != name || actual.GID <= 0 {
				return Group{}, errors.New("DSM group lookup returned an invalid identity")
			}
			return actual, nil
		}
	}
	return Group{}, ErrNotFound
}

func (c *Client) UserGroups(ctx context.Context, name string, uid int64) ([]string, error) {
	groups, err := c.Groups(ctx)
	if err != nil {
		return nil, err
	}
	membership := []string{}
	for _, g := range groups {
		var data struct {
			Users []struct {
				Name string `json:"name"`
				UID  int64  `json:"uid"`
			} `json:"users"`
			Total *int `json:"total"`
		}
		if err := c.Call(ctx, "SYNO.Core.Group.Member", 1, "list", url.Values{"group": {g.Name}, "ingroup": {"true"}, "offset": {"0"}, "limit": {"-1"}}, &data); err != nil {
			return nil, err
		}
		if data.Users == nil || data.Total == nil || *data.Total != len(data.Users) {
			return nil, errors.New("DSM group membership listing is incomplete")
		}
		for _, u := range data.Users {
			if u.Name == "" {
				return nil, errors.New("DSM group member has no account name")
			}
			if u.Name == name {
				if u.UID != uid {
					return nil, errors.New("account identity changed while reading membership")
				}
				membership = append(membership, g.Name)
			}
		}
	}
	slices.Sort(membership)
	return membership, nil
}

func (c *Client) SetUserGroups(ctx context.Context, name string, desired []string) error {
	c.mutation.Lock()
	defer c.mutation.Unlock()
	users, err := c.Users(ctx)
	if err != nil {
		return err
	}
	uid := int64(0)
	for _, u := range users {
		if u.Name == name {
			uid = u.UID
			break
		}
	}
	if uid <= 0 {
		return ErrNotFound
	}
	current, err := c.UserGroups(ctx, name, uid)
	if err != nil {
		return err
	}
	join, leave := []string{}, []string{}
	for _, g := range desired {
		if !slices.Contains(current, g) {
			join = append(join, g)
		}
	}
	for _, g := range current {
		if !slices.Contains(desired, g) {
			leave = append(leave, g)
		}
	}
	if len(join) == 0 && len(leave) == 0 {
		return nil
	}
	if !slices.Contains(desired, "users") || (c.IsSessionUser(name) && !slices.Contains(desired, "administrators")) {
		return errors.New("refusing to remove required account or management membership")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var task struct {
		ID string `json:"task_id"`
	}
	if err := c.Call(ctx, "SYNO.Core.User.Group", 1, "join", url.Values{"name": {name}, "join_group": {JSON(join)}, "leave_group": {JSON(leave)}}, &task); err != nil {
		return err
	}
	if task.ID == "" {
		return errors.New("DSM membership update returned no task identity; refresh before retrying")
	}
	for {
		var status struct {
			Finish  *bool `json:"finish"`
			Success *bool `json:"success"`
		}
		if err := c.Call(ctx, "SYNO.Core.User.Group", 1, "join_status", url.Values{"task_id": {task.ID}}, &status); err != nil {
			return err
		}
		if status.Finish == nil || status.Success == nil {
			return errors.New("DSM membership task status is incomplete")
		}
		if *status.Finish {
			if !*status.Success {
				return errors.New("DSM membership task failed; refresh before retrying")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("DSM membership update is unconfirmed; refresh before retrying")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func requireStrings(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		var value string
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) || json.Unmarshal(fields[name], &value) != nil {
			return errors.New("DSM metadata is missing a managed string value")
		}
	}
	return nil
}

func (c *Client) WriteGroup(ctx context.Context, method string, g Group) error {
	if method != "create" && method != "set" {
		return errors.New("unsupported group mutation")
	}
	return c.Mutate(ctx, "SYNO.Core.Group", 1, method, url.Values{"name": {g.Name}, "description": {g.Description}})
}

func (c *Client) DeletePrincipal(ctx context.Context, kind, name string) error {
	api := "SYNO.Core.User"
	value := name
	if kind == "group" {
		api = "SYNO.Core.Group"
		value = JSON([]string{name})
	} else if kind != "user" {
		return fmt.Errorf("unsupported principal kind %q", kind)
	}
	return c.Mutate(ctx, api, 1, "delete", url.Values{"name": {value}})
}
