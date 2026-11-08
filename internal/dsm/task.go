package dsm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type TaskReply struct {
	TaskID  string
	Finish  *bool
	Success *bool
	failed  bool
}

func (r *TaskReply) decodeEnvelope(data json.RawMessage, completion json.RawMessage) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return errors.New("task response must be an object")
	}
	var finish *bool
	if len(completion) != 0 {
		var value bool
		if json.Unmarshal(completion, &value) != nil || string(completion) == "null" {
			return errors.New("task completion is malformed")
		}
		finish = &value
	}
	*r = TaskReply{Finish: finish}
	if id, ok := raw["task_id"]; ok {
		if json.Unmarshal(id, &r.TaskID) != nil || !validTaskID(r.TaskID) {
			return errors.New("task identifier is malformed")
		}
	}
	if value, ok := raw["finish"]; ok {
		var inner bool
		if json.Unmarshal(value, &inner) != nil || string(value) == "null" || finish != nil && *finish != inner {
			return errors.New("task completion is malformed or inconsistent")
		}
		r.Finish = &inner
	}
	if value, ok := raw["success"]; ok {
		var success bool
		if json.Unmarshal(value, &success) != nil || string(value) == "null" {
			return errors.New("task success is malformed")
		}
		r.Success = &success
	}
	if value, ok := raw["error"]; ok {
		var failure struct {
			Code *int `json:"code"`
		}
		if json.Unmarshal(value, &failure) != nil || failure.Code == nil {
			return errors.New("task error is malformed")
		}
		r.failed = true
	}
	return nil
}

func (r TaskReply) Failed() bool {
	return r.failed || r.Success != nil && !*r.Success
}

func validTaskID(id string) bool {
	return id != "" && len(id) <= 256 && strings.IndexFunc(id, func(r rune) bool { return r < 32 || r == 127 }) < 0
}

func (c *Client) TaskStatus(ctx context.Context, api string, version int, id string) (TaskReply, error) {
	if !validTaskID(id) {
		return TaskReply{}, errors.New("invalid DSM task identifier")
	}
	var reply TaskReply
	if err := c.Call(ctx, api, version, "status", url.Values{"task_id": {id}}, &reply); err != nil {
		return TaskReply{}, err
	}
	if reply.TaskID != "" && reply.TaskID != id {
		return TaskReply{}, fmt.Errorf("DSM %s returned a different background task", api)
	}
	if reply.Finish == nil {
		return TaskReply{}, fmt.Errorf("DSM %s task status has no confirmed completion flag", api)
	}
	if reply.Failed() {
		return TaskReply{}, fmt.Errorf("DSM %s background task failed; inspect its status before reconciliation", api)
	}
	return reply, nil
}

func (c *Client) WaitTask(ctx context.Context, api string, version int, id string, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("invalid DSM task polling interval")
	}
	for {
		reply, err := c.TaskStatus(ctx, api, version, id)
		if err != nil {
			return err
		}
		if *reply.Finish {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (r TaskReply) ReportedError() bool { return r.failed }

type MutationReply struct {
	TaskReply
	code int64
}

func (r *MutationReply) decodeEnvelope(data json.RawMessage, completion json.RawMessage) error {
	if len(data) == 0 || string(data) == "null" {
		data = json.RawMessage(`{}`)
	}
	r.code = 0
	if err := r.TaskReply.decodeEnvelope(data, completion); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return errors.New("mutation response must be an object")
	}
	if value, ok := raw["code"]; ok && string(value) != "null" {
		if json.Unmarshal(value, &r.code) != nil {
			return errors.New("mutation result code is malformed")
		}
	}
	return nil
}
func (r MutationReply) Failed() bool { return r.TaskReply.Failed() || r.code != 0 }
