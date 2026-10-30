package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/proactive"
)

// ConnectorStatus is what is known about a connector for the active provider.
// The probe that fills it in is a later PR (CP-033 PR 5); until then every
// connector is ConnectorUnknown and enabling a preset needs a Yes.
type ConnectorStatus string

const (
	ConnectorUnknown      ConnectorStatus = "unknown"
	ConnectorReady        ConnectorStatus = "ready"
	ConnectorNotConnected ConnectorStatus = "not connected"
)

// ProactiveDeps wires /proactive to the preset service.
type ProactiveDeps struct {
	// Service resolves the preset service for the owner chat; an error means
	// the feature is unavailable right now (for example the owner chat is not
	// registered yet).
	Service func(context.Context) (*proactive.Service, error)
	// Connector reports the status of a connector key ("gmail" or "cal").
	// Optional: nil means every connector is unknown.
	Connector func(ctx context.Context, connector string) ConnectorStatus
	// ChatDir returns the host directory mounted at /workspace/chat for the
	// owner chat; the panel reads the agent's outcome files there. Optional:
	// nil or an error means no outcome is shown.
	ChatDir func(context.Context) (string, error)
	Now     func() time.Time // optional, defaults to time.Now
	Log     *slog.Logger     // optional
}

const proactiveUnavailable = "Proactive actions are unavailable right now."

const proactiveUsage = "Usage:\n" +
	"/proactive — overview\n" +
	"/proactive gmail|calendar [morning|urgent|evening | agenda|reminder|weekly] [on|off|freq <v>|time <HHMM>|lead <min>|reset]\n" +
	"/proactive tz [<IANA zone>]\n" +
	"/proactive hours [<HHMM-HHMM>]\n" +
	"/proactive quiet [<HHMM-HHMM>|off]\n" +
	"/proactive off | on"

// connectorTokens maps the command word to the catalog connector key.
var connectorTokens = []struct{ word, key, title string }{
	{"gmail", "gmail", "Gmail"},
	{"calendar", "cal", "Calendar"},
}

func connectorByWord(w string) (key, title string, ok bool) {
	for _, c := range connectorTokens {
		if c.word == w {
			return c.key, c.title, true
		}
	}
	return "", "", false
}

func connectorWord(key string) string {
	for _, c := range connectorTokens {
		if c.key == key {
			return c.word
		}
	}
	return key
}

func connectorTitle(key string) string {
	for _, c := range connectorTokens {
		if c.key == key {
			return c.title
		}
	}
	return key
}

// presetToken is the part of a preset id after the dot.
func presetToken(p proactive.Preset) string { return p.ID[strings.IndexByte(p.ID, '.')+1:] }

// RegisterProactiveCommand adds /proactive (CP-033). It is owner-only and
// private-chat only because the Router grants PermOwnerOnly to the owner chat
// alone: the owner inside a group is refused like everyone else. Every button
// re-dispatches the command, so the Router authorizes each press.
func RegisterProactiveCommand(r *Router, deps ProactiveDeps) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	h := proactiveHandler{deps: deps}
	r.Register("proactive", "proactive", "(owner) built-in recurring Gmail and Calendar actions", PermOwnerOnly, h.handle)
}

type proactiveHandler struct{ deps ProactiveDeps }

func (h proactiveHandler) connector(ctx context.Context, key string) ConnectorStatus {
	if h.deps.Connector == nil {
		return ConnectorUnknown
	}
	return h.deps.Connector(ctx, key)
}

func (h proactiveHandler) handle(ctx context.Context, cmd Command) (Response, error) {
	svc, err := h.deps.Service(ctx)
	if err != nil {
		return Response{Code: 1, Text: proactiveUnavailable}, fmt.Errorf("proactive: %w", err)
	}
	args := cmd.Args
	if len(args) == 0 {
		return h.overview(ctx, svc, "", 0)
	}
	switch args[0] {
	case "off", "on":
		if len(args) != 1 {
			return h.overview(ctx, svc, proactiveUsage, 1)
		}
		return h.globalPause(ctx, svc, args[0] == "off")
	case "tz":
		return h.timezone(ctx, svc, args[1:])
	case "hours":
		return h.hours(ctx, svc, args[1:])
	case "quiet":
		return h.quiet(ctx, svc, args[1:])
	case "check", "guide", "offer":
		return Response{Code: 1, Text: "/proactive " + args[0] + " is not available yet."}, nil
	}
	if key, _, ok := connectorByWord(args[0]); ok {
		if len(args) == 1 {
			return h.connectorScreen(ctx, svc, key, "", 0)
		}
		return h.preset(ctx, svc, cmd, key, args[1], args[2:])
	}
	return Response{Code: 1, Text: proactiveUsage}, nil
}

// failure turns an error from the service into a user-facing reply. ok is
// false for errors that are not part of the feature's contract (store
// failures and the like); the caller then answers generically and returns it.
func proactiveFailure(err error) (string, bool) {
	switch {
	case errors.Is(err, proactive.ErrInsideQuiet):
		return "That time is inside quiet hours. Pick another time, or change quiet hours with /proactive quiet.", true
	case errors.Is(err, proactive.ErrNoRunHours):
		return "Quiet hours leave no run hours inside the working window. Change /proactive hours or /proactive quiet.", true
	case errors.Is(err, proactive.ErrUserModified):
		return "This task was edited in /tasks, so /proactive will not overwrite it. Use Reset to template to take it back.", true
	case errors.Is(err, proactive.ErrGloballyPaused):
		return "All presets are paused (/proactive off). Send /proactive on first.", true
	case errors.Is(err, proactive.ErrTaskConflict):
		return "A task of another chat already holds this preset's id; it was left alone.", true
	case errors.Is(err, proactive.ErrNotEnabled):
		return "The preset is off. Turn it on first.", true
	}
	return "", false
}

// withNotice puts a line above a screen; code 1 marks a refusal.
func withNotice(r Response, notice string, code int) Response {
	if notice != "" {
		r.Text = notice + "\n\n" + r.Text
	}
	r.Code = code
	return r
}

// plain drops characters that would change Markdown or layout, and caps the length.
func plain(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune("`*_[]<>", r) {
			return -1
		}
		return r
	}, s)
	if len(s) > 40 {
		cut := 40
		for cut > 0 && !utf8.RuneStart(s[cut]) { // never split a rune
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// fixedTime renders HHMM as HH:MM.
func fixedTime(hhmm string) string {
	if len(hhmm) != 4 {
		return hhmm
	}
	return hhmm[:2] + ":" + hhmm[2:]
}

func minutesText(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func (h proactiveHandler) env(ctx context.Context, svc *proactive.Service) (proactive.Env, error) {
	tz, err := svc.Timezone(ctx)
	if err != nil {
		return proactive.Env{}, err
	}
	w, err := svc.Window(ctx)
	if err != nil {
		return proactive.Env{}, err
	}
	q, err := svc.Quiet(ctx)
	if err != nil {
		return proactive.Env{}, err
	}
	return proactive.Env{TZ: tz, Window: w, Quiet: q}, nil
}

func fail(err error) (Response, error) {
	return Response{Code: 1, Text: proactiveUnavailable}, fmt.Errorf("proactive: %w", err)
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && strconv.Itoa(n) == s
}
