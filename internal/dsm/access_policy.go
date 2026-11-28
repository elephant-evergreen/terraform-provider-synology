package dsm

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
)

type PartialAccessPolicyError struct{ Err error }

func (e *PartialAccessPolicyError) Error() string { return e.Err.Error() }
func (e *PartialAccessPolicyError) Unwrap() error { return e.Err }

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
		actual, err := c.groupDetails(ctx, g.Name)
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

func accessRank(access string) int {
	switch access {
	case "deny":
		return -1
	case "read_only":
		return 1
	case "read_write":
		return 2
	default:
		return 0
	}
}

func accessTarget(applied, current, desired map[string]string, restrictive bool) map[string]string {
	target := maps.Clone(applied)
	if target == nil {
		target = map[string]string{}
	}
	names := map[string]struct{}{}
	for name := range applied {
		names[name] = struct{}{}
	}
	for name := range current {
		names[name] = struct{}{}
	}
	for name := range desired {
		names[name] = struct{}{}
	}
	for name := range names {
		want, have := desired[name], current[name]
		if restrictive && accessRank(want) < accessRank(have) {
			target[name] = want
		}
		if !restrictive && accessRank(want) > accessRank(have) {
			target[name] = want
		}
	}
	return target
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
	type scope struct {
		kind    string
		desired map[string]string
		current map[string]string
		ids     map[string]int64
		items   []map[string]json.RawMessage
		applied map[string]string
	}
	scopes := []*scope{
		{"local_user", users, current.Users, current.UserIDs, nil, maps.Clone(current.Users)},
		{"local_group", groups, current.Groups, current.GroupIDs, nil, maps.Clone(current.Groups)},
	}
	for _, sc := range scopes {
		for name := range sc.desired {
			if sc.ids[name] <= 0 {
				return errors.New("a configured permission principal does not exist")
			}
		}
		items, err := c.permissions(ctx, share, sc.kind)
		if err != nil {
			return err
		}
		sc.items = items
	}
	write := func(sc *scope, target map[string]string) (bool, error) {
		if maps.Equal(target, sc.applied) {
			return false, nil
		}
		payload := make([]map[string]any, 0, len(sc.items))
		for _, item := range sc.items {
			entry, err := decodePermission(item)
			if err != nil {
				return false, err
			}
			access := target[entry.Name]
			row := map[string]any{"name": entry.Name, "is_readonly": access == "read_only", "is_writable": access == "read_write", "is_deny": access == "deny"}
			if inherit, ok := item["inherit"]; ok {
				row["inherit"] = inherit
			}
			payload = append(payload, row)
		}
		if err := c.Call(ctx, "SYNO.Core.Share.Permission", 1, "set", url.Values{"name": {share}, "user_group_type": {sc.kind}, "permissions": {JSON(payload)}}, nil); err != nil {
			return false, err
		}
		sc.applied = maps.Clone(target)
		return true, nil
	}
	wrote := false
	fail := func(err error) error {
		if wrote {
			return &PartialAccessPolicyError{Err: err}
		}
		return err
	}
	for _, sc := range scopes {
		changed, err := write(sc, accessTarget(sc.applied, sc.current, sc.desired, true))
		if err != nil {
			return fail(err)
		}
		wrote = wrote || changed
	}
	for _, sc := range scopes {
		changed, err := write(sc, accessTarget(sc.applied, sc.current, sc.desired, false))
		if err != nil {
			return fail(err)
		}
		wrote = wrote || changed
	}
	actual, err := c.AccessPolicy(ctx, share)
	if err != nil {
		return fail(err)
	}
	if current.ShareUUID != actual.ShareUUID || !maps.Equal(current.UserIDs, actual.UserIDs) || !maps.Equal(current.GroupIDs, actual.GroupIDs) {
		return fail(errors.New("DSM identities changed during permission update"))
	}
	if !maps.Equal(actual.Users, users) || !maps.Equal(actual.Groups, groups) {
		return fail(errors.New("DSM did not confirm the complete requested permission policy"))
	}
	return nil
}
