package dsm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskReplyEnvelopeValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		data       string
		completion string
		id         string
		finish     int
		failed     bool
		invalid    bool
	}{
		{name: "synchronous_ack", data: `{}`, finish: -1},
		{name: "asynchronous_ack", data: `{"task_id":"home-1"}`, id: "home-1", finish: -1},
		{name: "top_level_completion", data: `{"task_id":"home-1"}`, completion: `true`, id: "home-1", finish: 1},
		{name: "data_completion", data: `{"finish":false}`, finish: 0},
		{name: "consistent_completion", data: `{"finish":true}`, completion: `true`, finish: 1},
		{name: "conflicting_completion", data: `{"finish":false}`, completion: `true`, invalid: true},
		{name: "null_completion", data: `{"finish":null}`, invalid: true},
		{name: "null_top_completion", data: `{}`, completion: `null`, invalid: true},
		{name: "text_completion", data: `{}`, completion: `"true"`, invalid: true},
		{name: "empty_identifier", data: `{"task_id":""}`, invalid: true},
		{name: "numeric_identifier", data: `{"task_id":123}`, invalid: true},
		{name: "null_identifier", data: `{"task_id":null}`, invalid: true},
		{name: "control_identifier", data: `{"task_id":"home\n1"}`, invalid: true},
		{name: "missing_object", data: `null`, invalid: true},
		{name: "array", data: `[]`, invalid: true},
		{name: "nested_failure", data: `{"finish":true,"success":false}`, finish: 1, failed: true},
		{name: "zero_code_failure", data: `{"finish":true,"error":{"code":0}}`, finish: 1, failed: true},
		{name: "malformed_error", data: `{"finish":true,"error":{}}`, invalid: true},
		{name: "null_success", data: `{"finish":true,"success":null}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reply TaskReply
			err := reply.decodeEnvelope(json.RawMessage(tc.data), json.RawMessage(tc.completion))
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%t, error=%v", tc.invalid, err)
			}
			if err != nil {
				return
			}
			if reply.TaskID != tc.id || reply.Failed() != tc.failed || tc.finish == -1 && reply.Finish != nil || tc.finish >= 0 && (reply.Finish == nil || *reply.Finish != (tc.finish == 1)) {
				t.Fatal("task outcome was not represented exactly")
			}
		})
	}
}

func TestTaskPollingNeverRepeatsMutation(t *testing.T) {
	for _, mode := range []string{"complete", "missing_completion", "different_task", "task_failed", "zero_code_failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int64
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				if r.Form.Get("method") != "status" || r.Form.Get("task_id") != "home-1" {
					t.Error("polling attempted a mutation or changed task identity")
					w.WriteHeader(400)
					return
				}
				count := reads.Add(1)
				data := map[string]any{"task_id": "home-1", "finish": mode == "complete" && count > 1}
				switch mode {
				case "missing_completion":
					delete(data, "finish")
				case "different_task":
					data["task_id"] = "another-home-task"
				case "task_failed":
					data["success"] = false
				case "zero_code_failure":
					data["error"] = map[string]any{"code": 0}
				}
				json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
			})
			c.apis = map[string]Capability{"SYNO.Core.User.Home": {Path: "entry.cgi", Min: 1, Max: 1}}
			limit := 3 * time.Second
			if mode == "cancelled" {
				limit = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			err := c.WaitTask(ctx, "SYNO.Core.User.Home", 1, "home-1", time.Millisecond)
			if mode == "complete" {
				if err != nil || reads.Load() != 2 {
					t.Fatalf("completion: polls=%d error=%v", reads.Load(), err)
				}
			} else if err == nil {
				t.Fatal("unconfirmed or failed task was reported complete")
			}
			if mode == "cancelled" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation was lost: %v", err)
			}
		})
	}
}

func TestTaskEnvelopeDoesNotChangeOrdinaryResponses(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"finish":"unrelated-future-metadata","data":{"enabled":true}}`)
	})
	c.apis = map[string]Capability{"SYNO.Core.User.Home": {Path: "entry.cgi", Min: 1, Max: 1}}
	var ordinary map[string]bool
	if err := c.Call(context.Background(), "SYNO.Core.User.Home", 1, "get", nil, &ordinary); err != nil || !ordinary["enabled"] {
		t.Fatal("ordinary response decoding was changed by task support")
	}
	var reply TaskReply
	if err := c.Call(context.Background(), "SYNO.Core.User.Home", 1, "status", nil, &reply); err == nil {
		t.Fatal("task decoding accepted a nonboolean completion marker")
	}
}

func TestTaskAcknowledgementAndFailureAreBounded(t *testing.T) {
	var writes atomic.Int64
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writes.Add(1)
		fmt.Fprint(w, `{"success":true,"data":{"task_id":"home-1"}}`)
	})
	c.apis = map[string]Capability{"SYNO.Core.User.Home": {Path: "entry.cgi", Min: 1, Max: 1}}
	var reply TaskReply
	if err := c.MutateAfterReadResult(context.Background(), "SYNO.Core.User.Home", 1, "set", func() (url.Values, error) { return url.Values{"enable": {"true"}}, nil }, &reply); err != nil || reply.TaskID != "home-1" || writes.Load() != 1 {
		t.Fatal("acknowledgement was lost or mutation was repeated")
	}
	for _, id := range []string{"", strings.Repeat("x", 257), "bad\nidentifier"} {
		if err := c.WaitTask(context.Background(), "SYNO.Core.User.Home", 1, id, time.Second); err == nil {
			t.Fatal("invalid polling identity was accepted")
		}
	}
	if writes.Load() != 1 {
		t.Fatal("invalid polling parameters caused remote requests")
	}
}

func TestMutationReplyAllowsEmptyAcknowledgementWithoutWeakeningTasks(t *testing.T) {
	for _, tc := range []struct {
		name, body      string
		failed, invalid bool
	}{
		{"no_data", `{"success":true}`, false, false},
		{"null_data", `{"success":true,"data":null}`, false, false},
		{"empty_object", `{"success":true,"data":{}}`, false, false},
		{"result_code", `{"success":true,"data":{"code":42}}`, true, false},
		{"explicit_failure", `{"success":true,"data":{"success":false}}`, true, false},
		{"malformed_result", `{"success":true,"data":{"code":"bad"}}`, false, true},
		{"unknown_shape", `{"success":true,"data":[]}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.body) })
			c.apis = map[string]Capability{"SYNO.Core.Network": {Path: "entry.cgi", Min: 1, Max: 2}}
			var reply MutationReply
			err := c.Call(context.Background(), "SYNO.Core.Network", 2, "set", nil, &reply)
			if (err != nil) != tc.invalid {
				t.Fatalf("mutation validation: %v", err)
			}
			if err == nil && reply.Failed() != tc.failed {
				t.Fatal("mutation outcome was lost")
			}
			if tc.name == "no_data" || tc.name == "null_data" {
				var task TaskReply
				if err := c.Call(context.Background(), "SYNO.Core.Network", 2, "set", nil, &task); err == nil {
					t.Fatal("task response accepted missing task data")
				}
			}
		})
	}
}

func TestSynchronousMutationsRequireSuccessfulAcknowledgement(t *testing.T) {
	for _, body := range []string{
		`{"success":true}`,
		`{"success":true,"data":null}`,
		`{"success":true,"data":{"success":true}}`,
		`{"success":true,"data":{"success":false}}`,
		`{"success":true,"data":{"code":42}}`,
		`{"success":true,"data":{"task_id":"fixture-pending"}}`,
		`{"success":true,"data":{"finish":false}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var writes atomic.Int64
			c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) { writes.Add(1); fmt.Fprint(w, body) })
			c.apis = map[string]Capability{"SYNO.Core.Network.Proxy": {Path: "entry.cgi", Min: 1, Max: 1}}
			err := c.Mutate(context.Background(), "SYNO.Core.Network.Proxy", 1, "set", url.Values{"password": {"fixture-unreadable-secret"}})
			failure := strings.Contains(body, `false`) || strings.Contains(body, `42`) || strings.Contains(body, `task_id`)
			if (err != nil) != failure || writes.Load() != 1 {
				t.Fatalf("incorrect synchronous mutation outcome: %v, writes=%d", err, writes.Load())
			}
			if err != nil && strings.Contains(err.Error(), "fixture-unreadable-secret") {
				t.Fatal("mutation error exposed a secret")
			}
		})
	}
}
