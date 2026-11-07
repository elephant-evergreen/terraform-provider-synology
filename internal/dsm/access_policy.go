package dsm

import (
	"context"
	"errors"
	"maps"
	"net/url"
)

type AccessPolicy struct {
	ShareUUID string
	Users     map[string]string
	Groups    map[string]string
	UserIDs   map[string]int64
	GroupIDs  map[string]int64
}

func SameAccess(a, b map[string]string) bool { return maps.Equal(a, b) }

func (c *Client) AccessPolicy(ctx context.Context, share string) (AccessPolicy, error) {
	folder, err := c.Share(ctx, share)
	if err != nil {
		return AccessPolicy{}, err
	}
	if folder.UUID == "" {
		return AccessPolicy{}, errors.New("DSM returned no stable shared-folder identity")
	}
	p := AccessPolicy{ShareUUID: folder.UUID, Users: map[string]string{}, Groups: map[string]string{}, UserIDs: map[string]int64{}, GroupIDs: map[string]int64{}}
	users, err := c.Users(ctx)
	if err != nil {
		return p, err
	}
	for _, u := range users {
		p.UserIDs[u.Name] = u.UID
	}
	groups, err := c.Groups(ctx)
	if err != nil {
		return p, err
	}
	for _, g := range groups {
		actual, err := c.Group(ctx, g.Name)
		if err != nil {
			return p, err
		}
		p.GroupIDs[g.Name] = actual.GID
	}
	for _, kind := range []string{"local_user", "local_group"} {
		items, err := c.permissions(ctx, share, kind)
		if err != nil {
			return p, err
		}
		seen := map[string]bool{}
		for _, item := range items {
			entry, err := decodePermission(item)
			if err != nil {
				return p, err
			}
			if seen[entry.Name] {
				return p, errors.New("DSM returned duplicate permission principals")
			}
			seen[entry.Name] = true
			access, err := entry.Access()
			if err != nil {
				return p, err
			}
			ids, permissions := p.UserIDs, p.Users
			if kind == "local_group" {
				ids, permissions = p.GroupIDs, p.Groups
			}
			if ids[entry.Name] <= 0 {
				return p, errors.New("DSM permission principal has no confirmed numeric identity")
			}
			if access != "inherit" {
				permissions[entry.Name] = access
			}
		}
		ids := p.UserIDs
		if kind == "local_group" {
			ids = p.GroupIDs
		}
		if len(seen) != len(ids) {
			return p, errors.New("DSM permission listing does not cover every local principal")
		}
	}
	return p, nil
}

func ValidateAccessPolicy(users, groups map[string]string) error {
	if groups["administrators"] != "read_write" {
		return errors.New("administrators must retain read_write access")
	}
	for _, policy := range []map[string]string{users, groups} {
		for name, access := range policy {
			if name == "" || (access != "deny" && access != "read_only" && access != "read_write") {
				return errors.New("permission maps require named principals with deny, read_only or read_write; omit a principal for inheritance")
			}
		}
	}
	return nil
}

func (c *Client) WriteAccessPolicy(ctx context.Context, share string, expected AccessPolicy, users, groups map[string]string) error {
	if err := ValidateAccessPolicy(users, groups); err != nil {
		return err
	}
	c.mutation.Lock()
	defer c.mutation.Unlock()
	current, err := c.AccessPolicy(ctx, share)
	if err != nil {
		return err
	}
	if current.ShareUUID != expected.ShareUUID || !maps.Equal(current.UserIDs, expected.UserIDs) || !maps.Equal(current.GroupIDs, expected.GroupIDs) || !maps.Equal(current.Users, expected.Users) || !maps.Equal(current.Groups, expected.Groups) {
		return errors.New("DSM identities or permission policy changed before mutation; re-run plan")
	}
	scopes := []struct {
		kind             string
		desired, current map[string]string
		ids              map[string]int64
	}{{"local_user", users, current.Users, current.UserIDs}, {"local_group", groups, current.Groups, current.GroupIDs}}
	for _, scope := range scopes {
		for name := range scope.desired {
			if scope.ids[name] <= 0 {
				return errors.New("a configured permission principal does not exist")
			}
		}
	}
	for _, scope := range scopes {
		if maps.Equal(scope.desired, scope.current) {
			continue
		}
		items, err := c.permissions(ctx, share, scope.kind)
		if err != nil {
			return err
		}
		payload := make([]map[string]any, 0, len(items))
		for _, item := range items {
			entry, err := decodePermission(item)
			if err != nil {
				return err
			}
			access := scope.desired[entry.Name]
			row := map[string]any{"name": entry.Name, "is_readonly": access == "read_only", "is_writable": access == "read_write", "is_deny": access == "deny"}
			if inherit, ok := item["inherit"]; ok {
				row["inherit"] = inherit
			}
			payload = append(payload, row)
		}
		if err := c.Call(ctx, "SYNO.Core.Share.Permission", 1, "set", url.Values{"name": {share}, "user_group_type": {scope.kind}, "permissions": {JSON(payload)}}, nil); err != nil {
			return err
		}
	}
	actual, err := c.AccessPolicy(ctx, share)
	if err != nil {
		return err
	}
	if current.ShareUUID != actual.ShareUUID || !maps.Equal(current.UserIDs, actual.UserIDs) || !maps.Equal(current.GroupIDs, actual.GroupIDs) {
		return errors.New("DSM identities changed during permission update")
	}
	if !maps.Equal(actual.Users, users) || !maps.Equal(actual.Groups, groups) {
		return errors.New("DSM did not confirm the complete requested permission policy")
	}
	return nil
}
