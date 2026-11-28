package dsm

import (
	"encoding/json"
	"testing"
)

func FuzzTaskReplyDecode(f *testing.F) {
	f.Add([]byte(`{"task_id":"abc","finish":true,"success":true}`), []byte(""))
	f.Add([]byte(`{"success":false,"error":{"code":1}}`), []byte(""))
	f.Add([]byte(`{"finish":true}`), []byte("true"))
	f.Add([]byte(`{"task_id":"","finish":false}`), []byte("false"))
	f.Fuzz(func(t *testing.T, data, completion []byte) {
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil || raw == nil {
			return
		}
		var reply TaskReply
		if reply.decodeEnvelope(data, completion) != nil {
			return
		}
		if reply.TaskID != "" && !validTaskID(reply.TaskID) {
			t.Fatalf("accepted invalid task identifier %q", reply.TaskID)
		}
		if value, ok := raw["success"]; ok {
			var success bool
			if json.Unmarshal(value, &success) == nil && !success && !reply.Failed() {
				t.Fatalf("success=false decoded as a successful reply: %+v", reply)
			}
		}
		if _, ok := raw["error"]; ok && !reply.Failed() {
			t.Fatalf("error object decoded as a successful reply: %+v", reply)
		}
	})
}
