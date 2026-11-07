package dsm

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveReadOnly(t *testing.T) {
	if os.Getenv("SYNOLOGY_LIVE_READ") != "1" {
		t.Skip("explicit read-only live test not requested")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := New(os.Getenv("SYNOLOGY_ENDPOINT"), os.Getenv("SYNOLOGY_USERNAME"), os.Getenv("SYNOLOGY_PASSWORD"), os.Getenv("SYNOLOGY_OTP"), os.Getenv("SYNOLOGY_CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(ctx); err != nil {
			t.Errorf("cannot close live session: %v", err)
		}
	}()
	shares, err := c.Shares(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range shares {
		if _, err := c.Share(ctx, s.Name); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"local_user", "local_group"} {
			items, err := c.permissions(ctx, s.Name, kind)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if _, err := decodePermission(item); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	users, err := c.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		actual, err := c.User(ctx, u.Name)
		if err != nil {
			t.Fatal(err)
		}
		if len(actual.Groups) == 0 {
			t.Fatal("live account membership was not read")
		}
	}
	groups, err := c.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if _, err := c.Group(ctx, g.Name); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("Read-only client verified: %d shares, %d accounts, %d groups, explicit permissions and account membership", len(shares), len(users), len(groups))
}
