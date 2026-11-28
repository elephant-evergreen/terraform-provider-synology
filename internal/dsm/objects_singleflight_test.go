package dsm

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentUserListSharesOneRequest(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("api") == "SYNO.Core.User" && r.Form.Get("method") == "list" {
			calls.Add(1)
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(w, `{"success":true,"data":{"users":[{"name":"alice","uid":1001,"description":"","email":"","expired":"normal"}],"total":1}}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	client.apis = map[string]Capability{"SYNO.Core.User": {Path: "entry.cgi", Min: 1, Max: 1}}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			users, err := client.Users(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			if len(users) != 1 || users[0].Name != "alice" || users[0].UID != 1001 {
				t.Errorf("users = %+v", users)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("user list calls = %d, want 1", n)
	}
}

func TestConcurrentGroupListSharesOneRequest(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("api") == "SYNO.Core.Group" && r.Form.Get("method") == "list" {
			calls.Add(1)
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(w, `{"success":true,"data":{"groups":[{"name":"users","gid":100,"description":""}],"total":1}}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	client.apis = map[string]Capability{"SYNO.Core.Group": {Path: "entry.cgi", Min: 1, Max: 1}}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			groups, err := client.Groups(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			if len(groups) != 1 || groups[0].Name != "users" || groups[0].GID != 100 {
				t.Errorf("groups = %+v", groups)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("group list calls = %d, want 1", n)
	}
}
