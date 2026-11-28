package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func (f *fixture) passwordWriteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.passwordWrites
}

func TestUnacknowledgedPasswordWriteCannotReplay(t *testing.T) {
	ctx := context.Background()
	f := &fixture{
		shares:              map[string]map[string]any{},
		users:               map[string]map[string]any{"fixture": {"name": "fixture", "uid": 1027, "description": "initial", "email": "", "expired": "normal", "groups": []string{"users"}}},
		groups:              map[string]map[string]any{"users": {"name": "users", "gid": 100, "description": "System default group"}},
		permissions:         map[string]map[string]any{},
		loseUserPasswordAck: true,
	}
	server := configuredFixtureProtocol(t, f)
	r := NewUserResource()
	var schema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schema)
	prior := userModel{
		Name:               types.StringValue("fixture"),
		Description:        types.StringValue("initial"),
		Email:              types.StringValue(""),
		Expiration:         types.StringValue("normal"),
		UID:                types.Int64Value(1027),
		Groups:             types.SetValueMust(types.StringType, []attr.Value{types.StringValue("users")}),
		Password:           types.StringNull(),
		PasswordVersion:    types.Int64Value(1),
		DeletionProtection: types.BoolValue(true),
	}
	plan := prior
	plan.PasswordVersion = types.Int64Value(2)
	config := plan
	config.Password = types.StringValue("synthetic-rotated-password")
	encode := func(m userModel) *tfprotov6.DynamicValue {
		return protocolModel(t, tfsdk.State{Schema: schema.Schema}, m)
	}

	first, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     "synology_user",
		PriorState:   encode(prior),
		PlannedState: encode(plan),
		Config:       encode(config),
	})
	if err != nil || !protocolErrors(first.Diagnostics) {
		t.Fatalf("expected an unacknowledged password write: %v", err)
	}
	if n := f.passwordWriteCount(); n != 1 {
		t.Fatalf("first password write count = %d", n)
	}

	f.mu.Lock()
	f.loseUserPasswordAck = false
	f.mu.Unlock()

	second, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       "synology_user",
		PriorState:     first.NewState,
		PlannedState:   encode(plan),
		Config:         encode(config),
		PlannedPrivate: first.Private,
	})
	if err != nil || !protocolErrors(second.Diagnostics) {
		t.Fatalf("expected a reconciliation refusal: %v", err)
	}
	if n := f.passwordWriteCount(); n != 1 {
		t.Fatalf("unacknowledged password write was replayed: %d", n)
	}

	nextPlan := plan
	nextPlan.PasswordVersion = types.Int64Value(3)
	nextConfig := nextPlan
	nextConfig.Password = types.StringValue("synthetic-next-password")
	third, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       "synology_user",
		PriorState:     second.NewState,
		PlannedState:   encode(nextPlan),
		Config:         encode(nextConfig),
		PlannedPrivate: second.Private,
	})
	if err != nil || protocolErrors(third.Diagnostics) {
		t.Fatalf("expected a higher generation to rotate: %v", err)
	}
	if n := f.passwordWriteCount(); n != 2 {
		t.Fatalf("higher generation password write count = %d", n)
	}
}
