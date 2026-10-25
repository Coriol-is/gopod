package scheduler

import (
	"testing"
	"time"
)

func TestFirstRunAtExplicitZonesAndDST(t *testing.T) {
	for _, tc := range []struct{ now, zone, want string }{
		{"2026-01-02T00:00:00Z", "Europe/Belgrade", "2026-01-02T08:00:00Z"},
		{"2026-07-02T00:00:00Z", "Europe/Belgrade", "2026-07-02T07:00:00Z"},
		{"2026-10-24T10:00:00Z", "Europe/Belgrade", "2026-10-25T08:00:00Z"},
		{"2026-03-28T10:00:00Z", "Europe/Belgrade", "2026-03-29T07:00:00Z"},
		{"2026-07-02T00:00:00Z", "Europe/Moscow", "2026-07-02T06:00:00Z"},
	} {
		t.Run(tc.now+tc.zone, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.now)
			want, _ := time.Parse(time.RFC3339, tc.want)
			got, err := FirstRunAt("cron", "CRON_TZ="+tc.zone+" 0 9 * * *", now)
			if err != nil || got != want.UnixMilli() {
				t.Fatalf("next=%s err=%v want=%s", time.UnixMilli(got).UTC(), err, want)
			}
		})
	}
}

func TestFirstRunAtValidation(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-01-02T00:00:00Z")
	for _, tc := range []struct{ kind, value string }{
		{"cron", "CRON_TZ=Not/AZone 0 9 * * *"}, {"cron", "0 9 31 2 *"}, {"interval", "0s"}, {"interval", "1ns"}, {"interval", "-1h"}, {"once", "2025-01-01T00:00:00Z"}, {"once", "2027-01-01T09:00"}, {"bad", "1h"},
	} {
		if got, err := FirstRunAt(tc.kind, tc.value, now); err == nil || got != 0 {
			t.Errorf("accepted invalid %s %q: %d %v", tc.kind, tc.value, got, err)
		}
	}
	got, err := FirstRunAt("interval", "24h", now)
	if err != nil || got != now.Add(24*time.Hour).UnixMilli() {
		t.Fatal("interval wrong", got, err)
	}
	got, err = FirstRunAt("once", "2027-01-01T09:00:00+01:00", now)
	if err != nil || time.UnixMilli(got).UTC().Format(time.RFC3339) != "2027-01-01T08:00:00Z" {
		t.Fatal("once wrong", got, err)
	}
}
