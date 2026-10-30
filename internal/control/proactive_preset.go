package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Coriol-is/gopod/internal/proactive"
)

// preset handles /proactive <connector> <preset> [verb ...]. Every value is
// re-validated here, so a stale or forged button changes nothing it should not.
func (h proactiveHandler) preset(ctx context.Context, svc *proactive.Service, cmd Command, key, tok string, rest []string) (Response, error) {
	p, ok := proactive.Lookup(key + "." + tok)
	if !ok {
		return h.connectorScreen(ctx, svc, key, fmt.Sprintf("Unknown preset %q.", plain(tok)), 1)
	}
	screen := func(notice string, code int) (Response, error) { return h.presetScreen(ctx, svc, p, notice, code) }
	if len(rest) == 0 {
		return screen("", 0)
	}
	verb, vals := rest[0], rest[1:]
	if (verb == "on" || verb == "off" || verb == "reset") && len(vals) != 0 {
		return screen(proactiveUsage, 1)
	}
	switch verb {
	case "on":
		return h.enable(ctx, svc, cmd, p, screen)
	case "off":
		return h.apply(svc.Disable(ctx, p.ID), "Turned off.", screen)
	case "reset":
		return h.reset(ctx, svc, cmd, p, screen)
	case "freq", "time", "lead":
		if len(vals) == 0 && verb == "time" && len(p.TimeOptions(nil)) > 0 {
			return h.timePicker(ctx, svc, p)
		}
		if len(vals) != 1 {
			return screen(proactiveUsage, 1)
		}
		return h.configure(ctx, svc, p, verb, vals[0], screen)
	}
	return screen(fmt.Sprintf("Unknown option %q.\n%s", plain(verb), proactiveUsage), 1)
}

var errInvalidSetting = errors.New("invalid setting")

type screenFn func(notice string, code int) (Response, error)

// apply turns the result of a service call into the refreshed screen.
func (h proactiveHandler) apply(err error, done string, screen screenFn) (Response, error) {
	if err == nil {
		return screen(done, 0)
	}
	if msg, ok := proactiveFailure(err); ok {
		return screen(msg, 1)
	}
	return fail(err)
}

func (h proactiveHandler) enable(ctx context.Context, svc *proactive.Service, cmd Command, p proactive.Preset, screen screenFn) (Response, error) {
	st, err := svc.Status(ctx, p.ID)
	if err != nil {
		return fail(err)
	}
	if st.GloballyPaused {
		msg, _ := proactiveFailure(proactive.ErrGloballyPaused)
		return screen(msg, 1)
	}
	if st.State == proactive.StateActive {
		return screen("Already on.", 0)
	}
	title := connectorTitle(p.Connector())
	switch h.connector(ctx, p.Connector()) {
	case ConnectorNotConnected:
		return screen(title+" is not connected for the active provider, so the preset stays off.", 1)
	case ConnectorUnknown:
		if !cmd.Confirmed {
			return Confirm(cmd, fmt.Sprintf("The %s connector has not been checked yet. Turn on \"%s\" anyway? If it is not connected, runs will report it unavailable.", title, p.Title)), nil
		}
	}
	return h.apply(svc.Enable(ctx, p.ID), "Turned on.", screen)
}

func (h proactiveHandler) reset(ctx context.Context, svc *proactive.Service, cmd Command, p proactive.Preset, screen screenFn) (Response, error) {
	if !cmd.Confirmed {
		return Confirm(cmd, fmt.Sprintf("Reset \"%s\" to the template? Your edits to the task in /tasks are discarded.", p.Title)), nil
	}
	return h.apply(svc.ResetToTemplate(ctx, p.ID), "Reset to the template.", screen)
}

// configure changes one setting of a preset (on or off: the choice is stored).
// The read-modify-write happens inside the service's lock, so two quick presses
// on different fields cannot overwrite each other.
func (h proactiveHandler) configure(ctx context.Context, svc *proactive.Service, p proactive.Preset, field, value string, screen screenFn) (Response, error) {
	if field == "lead" && len(p.Leads()) == 0 {
		return screen(p.Title+" has no reminder lead.", 1)
	}
	lead := 0
	if field == "lead" {
		n, ok := atoi(value)
		if !ok {
			return screen(fmt.Sprintf("%q is not a number of minutes.", plain(value)), 1)
		}
		lead = n
	}
	err := svc.UpdateSettings(ctx, p.ID, func(s *proactive.Settings) error {
		switch field {
		case "freq":
			s.Freq = value
		case "time":
			s.Time = value
		case "lead":
			s.Lead = lead
		}
		if p.ValidateSettings(*s) != nil {
			return errInvalidSetting // nothing is written
		}
		return nil
	})
	if errors.Is(err, errInvalidSetting) {
		return screen(invalidValue(p, field, value), 1)
	}
	return h.apply(err, "Updated.", screen)
}

func invalidValue(p proactive.Preset, field, value string) string {
	switch field {
	case "freq":
		return fmt.Sprintf("%q is not offered here. Use: %s.", plain(value), strings.Join(p.Freqs(), ", "))
	case "lead":
		var ls []string
		for _, l := range p.Leads() {
			ls = append(ls, fmt.Sprint(l))
		}
		return fmt.Sprintf("Lead %q is not offered. Use: %s minutes.", plain(value), strings.Join(ls, ", "))
	}
	opts := p.TimeOptions(nil)
	if len(opts) == 0 {
		return fmt.Sprintf("%s has no time of day.", p.Title)
	}
	return fmt.Sprintf("Time %q is not valid. Use HHMM from %s to %s.", plain(value), opts[0], opts[len(opts)-1])
}
