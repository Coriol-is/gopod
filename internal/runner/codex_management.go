package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"
)

const managementTimeout = 10 * time.Second
const managementCleanupReserve = 3 * time.Second

// ManagementReport contains normalized management-plane observations only.
// It does not prove inference is available, nor authorize a provider switch.
type ManagementReport struct {
	Provider, Auth, Quota, QuotaScope, ErrorClass, Inference string
	CheckedAt                                                time.Time
	OrdinaryUsageAllowed, SpendControlReached                *bool
	Primary, Secondary                                       *ManagementWindow
}
type ManagementWindow struct {
	UsedPercent                  *int
	ResetsAt, WindowDurationMins *int64
}

type managementProbe func(context.Context, string) (ManagementReport, error)

func (r *Runner) ManagementCheck(ctx context.Context, folder string) ManagementReport {
	return r.managementCheck(ctx, folder, func(ctx context.Context) (string, error) {
		if r.d == nil {
			return "", errors.New("unavailable")
		}
		id, err := r.d.inspectByName(ctx, ContainerName(folder))
		if err != nil || id == "" {
			return "", errors.New("unavailable")
		}
		info, err := r.d.cli.ContainerInspect(ctx, id)
		if err != nil || info.State == nil || !info.State.Running {
			return "", errors.New("unavailable")
		}
		return id, nil
	}, r.probeCodexManagement)
}

func (r *Runner) managementCheck(ctx context.Context, folder string, inspect func(context.Context) (string, error), probe managementProbe) ManagementReport {
	deadline := time.Now().Add(managementTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	report := ManagementReport{Auth: "unknown", Quota: "unknown", ErrorClass: "none", Inference: "unverified", CheckedAt: time.Now().UTC()}
	release, err := r.acquireAgentSlot(ctx, folder)
	if err != nil {
		report.ErrorClass = managementFailure(ctx)
		return report
	}
	defer release()
	if r.selectionBlocked(folder) {
		report.ErrorClass = "selection_blocked"
		return report
	}
	prov := r.ProviderForChat(folder)
	report.Provider = prov.Name()
	if report.Provider != "codex" {
		report.ErrorClass = "unsupported"
		return report
	}
	work, stop := context.WithDeadline(ctx, deadline.Add(-managementCleanupReserve))
	defer stop()
	if work.Err() != nil {
		report.ErrorClass = managementFailure(work)
		return report
	}
	id, err := inspect(work)
	if err != nil {
		report.ErrorClass = managementFailure(work)
		return report
	}
	if work.Err() != nil {
		report.ErrorClass = managementFailure(work)
		return report
	}
	observed, err := probe(work, id)
	observed.Provider = report.Provider
	observed.CheckedAt = time.Now().UTC()
	observed.Inference = "unverified"
	if err != nil {
		if observed.ErrorClass == "" || observed.ErrorClass == "none" {
			observed.ErrorClass = managementFailure(work)
		}
		if observed.Auth == "" {
			observed.Auth = "unknown"
		}
		if observed.Quota == "" {
			observed.Quota = "unknown"
		}
	}
	return observed
}
func managementFailure(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if ctx.Err() != nil {
		return "canceled"
	}
	return "transport"
}

// managementRPC allows exactly initialization and the two read-only account
// methods. There is no thread/turn/login/tool method in this protocol path.
func codexManagementProtocol(ctx context.Context, input io.Writer, output io.Reader) (ManagementReport, error) {
	report := ManagementReport{Auth: "unknown", Quota: "unknown", ErrorClass: "none", Inference: "unverified"}
	scanner := bufio.NewScanner(io.LimitReader(output, 1024*1024+1))
	scanner.Buffer(make([]byte, 4096), 256*1024)
	rpc := managementRPC{ctx: ctx, in: json.NewEncoder(input), out: scanner}
	if _, err := rpc.call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "gopod", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": false}}); err != nil {
		return managementProtocolError(report, err)
	}
	if err := rpc.in.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return report, err
	}
	raw, err := rpc.call(2, "account/read", map[string]bool{"refreshToken": false})
	if err != nil {
		return managementProtocolError(report, err)
	}
	var response struct {
		Account json.RawMessage `json:"account"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Account) == 0 {
		return managementSchemaError(report)
	}
	if string(response.Account) == "null" {
		report.Auth = "not_authenticated"
		return report, nil
	}
	var account struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(response.Account, &account) != nil || account.Type == "" {
		return managementSchemaError(report)
	}
	switch account.Type {
	case "apiKey":
		report.Auth = "authenticated_api_key"
		report.Quota = "not_applicable"
		return report, nil
	case "chatgpt":
		report.Auth = "authenticated_chatgpt"
	default:
		report.ErrorClass = "unsupported"
		return report, nil
	}
	raw, err = rpc.call(3, "account/rateLimits/read", map[string]any{})
	if err != nil {
		return managementProtocolError(report, err)
	}
	var limits struct {
		Ordinary *bool `json:"ordinaryUsageAllowed"`
		Limits   *struct {
			Spend              *bool `json:"spendControlReached"`
			Primary, Secondary *ManagementWindowWire
		} `json:"rateLimits"`
	}
	if json.Unmarshal(raw, &limits) != nil || limits.Limits == nil {
		return managementSchemaError(report)
	}
	report.OrdinaryUsageAllowed = limits.Ordinary
	report.SpendControlReached = limits.Limits.Spend
	report.Primary = normalizeManagementWindow(limits.Limits.Primary)
	report.Secondary = normalizeManagementWindow(limits.Limits.Secondary)
	report.Quota = "observed"
	report.QuotaScope = "default_bucket"
	return report, nil
}

type ManagementWindowWire struct {
	UsedPercent        *int   `json:"usedPercent"`
	ResetsAt           *int64 `json:"resetsAt"`
	WindowDurationMins *int64 `json:"windowDurationMins"`
}

func normalizeManagementWindow(w *ManagementWindowWire) *ManagementWindow {
	if w == nil {
		return nil
	}
	out := &ManagementWindow{}
	if w.UsedPercent != nil && *w.UsedPercent >= 0 && *w.UsedPercent <= 100 {
		out.UsedPercent = w.UsedPercent
	}
	if w.ResetsAt != nil && *w.ResetsAt >= 0 {
		out.ResetsAt = w.ResetsAt
	}
	if w.WindowDurationMins != nil && *w.WindowDurationMins >= 0 && *w.WindowDurationMins <= 525600 {
		out.WindowDurationMins = w.WindowDurationMins
	}
	return out
}

var errManagementRPC = errors.New("management rpc rejected")
var errManagementSchema = errors.New("management invalid schema")

func managementSchemaError(r ManagementReport) (ManagementReport, error) {
	r.ErrorClass = "schema"
	return r, errManagementSchema
}
func managementProtocolError(r ManagementReport, err error) (ManagementReport, error) {
	if errors.Is(err, errManagementRPC) {
		r.ErrorClass = "api_rejection"
		return r, nil
	}
	if errors.Is(err, errManagementSchema) {
		return managementSchemaError(r)
	}
	return r, err
}

type managementRPC struct {
	ctx          context.Context
	in           *json.Encoder
	out          *bufio.Scanner
	count, bytes int
}

func (r *managementRPC) call(id int, method string, params any) (json.RawMessage, error) {
	if method != "initialize" && method != "account/read" && method != "account/rateLimits/read" {
		return nil, errManagementSchema
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.in.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for r.out.Scan() {
		line := r.out.Bytes()
		r.count++
		r.bytes += len(line)
		if r.count > 64 || r.bytes > 1024*1024 {
			return nil, errManagementSchema
		}
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(line, &msg) != nil {
			return nil, errManagementSchema
		}
		if len(msg.ID) == 0 {
			continue
		} // bounded, ignored server notifications
		if string(msg.ID) != strconv.Itoa(id) {
			return nil, errManagementSchema
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, errManagementRPC
		}
		if len(msg.Result) == 0 || string(msg.Result) == "null" {
			return nil, errManagementSchema
		}
		return msg.Result, nil
	}
	if r.out.Err() != nil {
		return nil, errManagementSchema
	}
	return nil, io.ErrUnexpectedEOF
}
