package dsm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestAccessPolicyReadsOneGroupListAndConfirmsEveryIdentity(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed=%t", malformed), func(t *testing.T) {
			var lists, details atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				api, method := r.Form.Get("api"), r.Form.Get("method")
				var data string
				switch api + "." + method {
				case "SYNO.Core.Share.list":
					data = `{"shares":[{"name":"test-share"}],"total":1}`
				case "SYNO.Core.Share.get":
					data = `{"name":"test-share","uuid":"test-uuid","desc":"","vol_path":"/volume1","hidden":false,"hide_unreadable":false,"enable_recycle_bin":false,"recycle_bin_admin_only":false}`
				case "SYNO.Core.User.list":
					data = `{"users":[],"total":0}`
				case "SYNO.Core.Group.list":
					lists.Add(1)
					data = `{"groups":[{"name":"first"},{"name":"second"}],"total":2}`
				case "SYNO.Core.Group.get":
					var names []string
					if json.Unmarshal([]byte(r.Form.Get("name")), &names) != nil || len(names) != 1 {
						t.Error("Expected a single group lookup")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					id := details.Add(1)
					if malformed && names[0] == "second" {
						id = 0
					}
					data = fmt.Sprintf(`{"groups":[{"name":%q,"description":"","gid":%d}],"errors":[]}`, names[0], id)
				case "SYNO.Core.Share.Permission.list":
					data = `{"items":[],"total":0}`
					if r.Form.Get("user_group_type") == "local_group" {
						data = `{"items":[{"name":"first","is_readonly":true,"is_writable":false,"is_deny":false},{"name":"second","is_readonly":false,"is_writable":false,"is_deny":false}],"total":2}`
					}
				default:
					t.Errorf("Unexpected DSM API call %s.%s", api, method)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				fmt.Fprintf(w, `{"success":true,"data":%s}`, data)
			})
			client.apis = map[string]Capability{}
			for _, api := range []string{"SYNO.Core.Share", "SYNO.Core.User", "SYNO.Core.Group", "SYNO.Core.Share.Permission"} {
				client.apis[api] = Capability{Path: "entry.cgi", Min: 1, Max: 1}
			}
			policy, err := client.AccessPolicy(t.Context(), "test-share")
			if malformed {
				if err == nil || err.Error() != "DSM group lookup returned an invalid identity" {
					t.Errorf("Error = %v, want invalid group identity", err)
				}
			} else if err != nil || !reflect.DeepEqual(policy.GroupIDs, map[string]int64{"first": 1, "second": 2}) ||
				!reflect.DeepEqual(policy.Groups, map[string]string{"first": "read_only"}) {
				t.Errorf("Access policy = %+v, %v", policy, err)
			}
			if lists.Load() != 1 || details.Load() != 2 {
				t.Errorf("Group calls = %d list and %d get, want 1 list and 2 get", lists.Load(), details.Load())
			}
		})
	}
}

func TestAccessTargetRestrictsBeforeGranting(t *testing.T) {
	current := map[string]string{"writer": "read_write", "reader": "read_only", "denied": "deny"}
	desired := map[string]string{"writer": "read_only", "reader": "read_write", "denied": ""}

	restrictive := accessTarget(current, current, desired, true)
	if restrictive["writer"] != "read_only" || restrictive["reader"] != "read_only" || restrictive["denied"] != "deny" {
		t.Fatalf("restrictive phase = %v", restrictive)
	}
	permissive := accessTarget(restrictive, current, desired, false)
	if permissive["writer"] != "read_only" || permissive["reader"] != "read_write" || permissive["denied"] != "" {
		t.Fatalf("permissive phase = %v", permissive)
	}
}

func TestAccessTargetAddsDenyBeforeGrant(t *testing.T) {
	current := map[string]string{}
	desired := map[string]string{"denied": "deny", "granted": "read_write"}

	restrictive := accessTarget(current, current, desired, true)
	if restrictive["denied"] != "deny" || restrictive["granted"] != "" {
		t.Fatalf("restrictive phase = %v", restrictive)
	}
	permissive := accessTarget(restrictive, current, desired, false)
	if permissive["denied"] != "deny" || permissive["granted"] != "read_write" {
		t.Fatalf("permissive phase = %v", permissive)
	}
}

func TestWriteAccessPolicyReportsPartialFailure(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		api, method := r.Form.Get("api"), r.Form.Get("method")
		var data string
		switch api + "." + method {
		case "SYNO.Core.Share.list":
			data = `{"shares":[{"name":"share"}],"total":1}`
		case "SYNO.Core.Share.get":
			data = `{"name":"share","uuid":"share-uuid","desc":"","vol_path":"/volume1","hidden":false,"hide_unreadable":false,"enable_recycle_bin":false,"recycle_bin_admin_only":false}`
		case "SYNO.Core.User.list":
			data = `{"users":[{"name":"alice","uid":1001,"description":"","email":"","expired":"normal"}],"total":1}`
		case "SYNO.Core.Group.list":
			data = `{"groups":[{"name":"administrators"},{"name":"readers"}],"total":2}`
		case "SYNO.Core.Group.get":
			var names []string
			if json.Unmarshal([]byte(r.Form.Get("name")), &names) != nil || len(names) != 1 {
				t.Error("expected one group name")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			gid := 101
			if names[0] == "readers" {
				gid = 102
			}
			data = fmt.Sprintf(`{"groups":[{"name":%q,"gid":%d,"description":""}]}`, names[0], gid)
		case "SYNO.Core.Share.Permission.list":
			if r.Form.Get("user_group_type") == "local_user" {
				data = `{"items":[{"name":"alice","is_readonly":false,"is_writable":false,"is_deny":false}],"total":1}`
			} else {
				data = `{"items":[{"name":"administrators","is_readonly":false,"is_writable":true,"is_deny":false},{"name":"readers","is_readonly":false,"is_writable":false,"is_deny":false}],"total":2}`
			}
		case "SYNO.Core.Share.Permission.set":
			if r.Form.Get("user_group_type") == "local_group" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			data = `{}`
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":true,"data":%s}`, data)
	})
	client.apis = map[string]Capability{}
	for _, api := range []string{"SYNO.Core.Share", "SYNO.Core.User", "SYNO.Core.Group", "SYNO.Core.Share.Permission"} {
		client.apis[api] = Capability{Path: "entry.cgi", Min: 1, Max: 1}
	}
	expected := AccessPolicy{
		ShareUUID: "share-uuid",
		Users:     map[string]string{},
		Groups:    map[string]string{"administrators": "read_write"},
		UserIDs:   map[string]int64{"alice": 1001},
		GroupIDs:  map[string]int64{"administrators": 101, "readers": 102},
	}
	err := client.WriteAccessPolicy(t.Context(), "share", expected, map[string]string{"alice": "read_write"}, map[string]string{"administrators": "read_write", "readers": "read_only"})
	var partial *PartialAccessPolicyError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v, want PartialAccessPolicyError", err)
	}
}
