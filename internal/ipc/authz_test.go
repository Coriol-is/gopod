package ipc

import (
	"errors"
	"testing"
)

func TestAuthzMessage(t *testing.T) {
	owner := "owner"
	resolve := staticResolver(map[string]string{
		"owner": "1001",
		"alice": "2002",
	})

	cases := []struct {
		name    string
		source  string
		jid     string
		wantErr bool
	}{
		{"alice → own jid", "alice", "2002", false},
		{"alice → owner jid (cross-chat blocked)", "alice", "1001", true},
		{"alice → unregistered jid", "alice", "9999", true},
		{"owner → alice", owner, "2002", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := authzMessage(c.source, c.jid, owner, resolve)
			gotErr := err != nil
			if gotErr != c.wantErr {
				t.Fatalf("authzMessage(%q, %q) err=%v, wantErr=%v",
					c.source, c.jid, err, c.wantErr)
			}
			if c.wantErr && !errors.Is(err, ErrPermanent) {
				t.Errorf("authzMessage(%q, %q) err=%v does not match ErrPermanent",
					c.source, c.jid, err)
			}
		})
	}
}

func TestAuthzTaskScheduleTarget(t *testing.T) {
	owner := "owner"
	resolve := staticResolver(map[string]string{
		"owner": "1001",
		"alice": "2002",
	})

	cases := []struct {
		name    string
		source  string
		target  string // empty = same as source
		wantErr bool
	}{
		{"alice → alice", "alice", "alice", false},
		{"alice → empty (defaults to source)", "alice", "", false},
		{"alice → owner (cross-chat blocked)", "alice", "owner", true},
		{"owner → alice", owner, "alice", false},
		{"owner → unregistered", owner, "ghost", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := authzTaskSchedule(c.source, c.target, owner, resolve)
			gotErr := err != nil
			if gotErr != c.wantErr {
				t.Fatalf("authzTaskSchedule(%q, %q) err=%v, wantErr=%v",
					c.source, c.target, err, c.wantErr)
			}
			if c.wantErr && !errors.Is(err, ErrPermanent) {
				t.Errorf("authzTaskSchedule(%q, %q) err=%v does not match ErrPermanent",
					c.source, c.target, err)
			}
		})
	}
}
