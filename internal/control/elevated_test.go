package control

import (
	"context"
	"strings"
	"testing"
)

type rootProbe struct {
	starts int
	active bool
}

func (p *rootProbe) Active() bool   { return p.active }
func (p *rootProbe) Status() string { return "status" }
func (p *rootProbe) Start(context.Context, string) (string, error) {
	p.starts++
	p.active = true
	return "started", nil
}
func (p *rootProbe) Close(context.Context, string) error      { p.active = false; return nil }
func (p *rootProbe) Log(context.Context, int) (string, error) { return "log", nil }
func (p *rootProbe) StopAnswer() bool                         { return false }
func TestRootPermissionConfirmationAndStaleFence(t *testing.T) {
	r := New(nil)
	p := &rootProbe{}
	RegisterElevated(r, 123, p)
	good := Caller{IsOwner: true, Source: SourceTelegram, ChatKind: "private", ChatID: 123, UserID: 123, MessageID: 12}
	for _, mutate := range []func(*Caller){func(c *Caller) { c.Source = SourceCLI }, func(c *Caller) { c.ChatKind = "group" }, func(c *Caller) { c.ChatID = 456 }, func(c *Caller) { c.UserID = 456 }, func(c *Caller) { c.MessageID = 0 }, func(c *Caller) { c.IsOwner = false }} {
		c := good
		mutate(&c)
		resp, err := r.Dispatch(t.Context(), Command{Name: "root", Args: []string{"claude"}, Caller: c, Confirmed: true})
		if err == nil && resp.Code == 0 {
			t.Fatal("accepted forged scope")
		}
	}
	cmd := Command{Name: "root", Args: []string{"claude"}, Caller: good}
	resp, err := r.Dispatch(t.Context(), cmd)
	if err != nil || resp.Code != CodeConfirm || p.starts != 0 || !strings.Contains(resp.Text, "полный доступ") {
		t.Fatal(resp, err)
	}
	confirm := cmd
	confirm.Args = resp.Buttons[0][0].Args
	confirm.Confirmed = true
	if _, err = r.Dispatch(t.Context(), confirm); err != nil || p.starts != 1 {
		t.Fatal(err)
	}
	_, _ = r.Dispatch(t.Context(), Command{Name: "root", Args: []string{"stop"}, Caller: good})
	resp, err = r.Dispatch(t.Context(), confirm)
	if err != nil || resp.Code == 0 || p.starts != 1 {
		t.Fatal("stale confirmation reopened", resp, err)
	}
}

func TestRootCancelNeverClaimsActiveSessionWasNotCreated(t *testing.T) {
	r := New(nil)
	p := &rootProbe{active: true}
	RegisterElevated(r, 123, p)
	resp, err := r.Dispatch(t.Context(), Command{Name: "root", Args: []string{"cancel"}, Caller: Caller{IsOwner: true, Source: SourceTelegram, ChatKind: "private", ChatID: 123, UserID: 123, MessageID: 12}})
	if err != nil || strings.Contains(resp.Text, "не создана") || !strings.Contains(resp.Text, "/root stop") {
		t.Fatal(resp, err)
	}
}

func (p *rootProbe) Generation() string {
	if p.active {
		return "live-generation"
	}
	return ""
}
func TestRootStaleCloseButtonCannotCloseNewGeneration(t *testing.T) {
	r := New(nil)
	p := &rootProbe{active: true}
	RegisterElevated(r, 123, p)
	resp, err := r.Dispatch(t.Context(), Command{Name: "root", Args: []string{"stop", "old-generation"}, Caller: Caller{IsOwner: true, Source: SourceTelegram, ChatKind: "private", ChatID: 123, UserID: 123, MessageID: 12}})
	if err != nil || !p.active || !strings.Contains(resp.Text, "завершённой") {
		t.Fatal(resp, err)
	}
}

func TestRootLoginRequiresOwnerActiveSessionAndCurrentGeneration(t *testing.T) {
	r := New(nil)
	p := &rootProbe{}
	calls := 0
	RegisterElevated(r, 123, p, func(context.Context, Command) (Response, error) { calls++; return Response{Text: "login"}, nil })
	caller := Caller{IsOwner: true, Source: SourceTelegram, ChatKind: "private", ChatID: 123, UserID: 123, MessageID: 12}
	cmd := Command{Name: "root", Args: []string{"login"}, Caller: caller}
	res, _ := r.Dispatch(t.Context(), cmd)
	if res.Code == 0 || calls != 0 {
		t.Fatal("inactive login accepted")
	}
	p.active = true
	cmd.Args = []string{"login", "old-generation"}
	res, _ = r.Dispatch(t.Context(), cmd)
	if res.Code == 0 || calls != 0 {
		t.Fatal("stale login accepted")
	}
	cmd.Args = []string{"login", "live-generation"}
	res, _ = r.Dispatch(t.Context(), cmd)
	if res.Code != 0 || calls != 1 {
		t.Fatal(res, calls)
	}
	cmd.Caller.UserID = 999
	res, _ = r.Dispatch(t.Context(), cmd)
	if res.Code == 0 || calls != 1 {
		t.Fatal("wrong actor accepted")
	}
}

func TestRootLoginLateBindingDoesNotRegisterCommandTwice(t *testing.T) {
	r := New(nil)
	p := &rootProbe{active: true}
	bind := RegisterElevated(r, 123, p)
	calls := 0
	bind(func(context.Context, Command) (Response, error) { calls++; return Response{Text: "bound"}, nil })
	res, err := r.Dispatch(t.Context(), Command{Name: "root", Args: []string{"login"}, Caller: Caller{IsOwner: true, Source: SourceTelegram, ChatKind: "private", ChatID: 123, UserID: 123, MessageID: 10}})
	if err != nil || res.Text != "bound" || calls != 1 {
		t.Fatal(res, err, calls)
	}
	roots := 0
	for _, c := range r.List() {
		if c.Name == "root" {
			roots++
		}
	}
	if roots != 1 {
		t.Fatal("root registration count", roots)
	}
}
