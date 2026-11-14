package dsm

import (
	"encoding/json"
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
