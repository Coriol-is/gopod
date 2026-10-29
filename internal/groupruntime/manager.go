package groupruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/pkg/stdcopy"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/groupsandbox"
	"github.com/Coriol-is/gopod/internal/isolationproxy"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-units"
)

const revision = "group-boundary-v1"

// AdmissionError exposes only a fixed structural stage, never upstream output.
type AdmissionError struct{ Stage, Detail string }

func (e *AdmissionError) Error() string { return "group admission unavailable: " + e.Stage }
func (e *AdmissionError) Unwrap() error { return ErrUnavailable }

type Manager struct {
	Config           Config
	Store            *store.Store
	Docker           *client.Client
	DataDir, Version string
	UID, GID         int
	Log              *slog.Logger
	MCP              *groupsandbox.MCP
	Request          func(context.Context, store.ScopeRef, int, json.RawMessage) (string, error)
	// Slot serializes owner-visible lifecycle steps with foreground work (the
	// runner chat slot); nil runs them directly. Notify sends a private owner
	// notice; nil drops it.
	Slot     func(ctx context.Context, folder string, op func(context.Context) (string, error)) (string, error)
	Notify   func(context.Context, string)
	auto     autoState
	mu       sync.Mutex
	locks    map[string]chan struct{}
	disabled map[string]bool
}

func (m *Manager) lock(ctx context.Context, id string) (func(), error) {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = map[string]chan struct{}{}
	}
	ch := m.locks[id]
	if ch == nil {
		ch = make(chan struct{}, 1)
		m.locks[id] = ch
	}
	m.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ErrUnavailable
	}
}
func (m *Manager) Authorize(ctx context.Context, c store.RegisteredChat) error {
	if m.UID <= 0 || m.GID <= 0 || m.Store == nil || m.Docker == nil || m.MCP == nil || m.Config.Qualification == "" || c.IsOwner || !c.GroupEpochReady || !store.ValidScopeIdentity(c.Scope()) {
		return ErrUnavailable
	}
	enabled := m.groupAllowed(ctx, c)
	m.mu.Lock()
	blocked := m.disabled[c.JID]
	m.mu.Unlock()
	if unknown, err := m.Store.GetState(ctx, "group_cleanup_unknown:"+c.NamespaceID); err != nil || unknown == "1" {
		return ErrUnavailable
	}
	if disabled, err := m.Store.GetState(ctx, "group_disabled:"+c.NamespaceID); err != nil || disabled == "1" {
		return ErrUnavailable
	}
	if !enabled || blocked {
		return ErrUnavailable
	}
	if err := m.Store.ValidateScope(ctx, c.Scope()); err != nil {
		return ErrUnavailable
	}
	if known, err := m.Store.IsKnownGroup(ctx, c.JID); err != nil || !known {
		return ErrUnavailable
	}
	paths, err := runner.ValidateNamespacePaths(m.DataDir, c.Scope(), m.UID)
	if err != nil {
		return ErrUnavailable
	}

	return m.checkTree(ctx, paths.Workspace, workspaceCheckPolicy)
}
func (m *Manager) Start(ctx context.Context, c store.RegisteredChat, base runner.AgentProvider, thread int, selectedModel string) (id string, prov runner.AgentProvider, release func(), retErr error) {
	stage := "admission"
	defer func() {
		if retErr != nil {
			retErr = newAdmissionError(stage, retErr)
		}
	}()

	if err := m.Authorize(ctx, c); err != nil {
		return "", nil, func() {}, err
	}
	unlock, err := m.lock(ctx, c.NamespaceID)
	if err != nil {
		return "", nil, func() {}, ErrUnavailable
	}
	cleanups := []func(){unlock}
	var once sync.Once
	var leaseToken string
	var cancelLease context.CancelFunc
	drainUnknown := false
	release = func() {
		once.Do(func() {
			if cancelLease != nil {
				cancelLease()
			}
			if leaseToken != "" {
				cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				if m.MCP.RevokeAndWait(cleanup, leaseToken) != nil {
					drainUnknown = true
					_ = m.Store.SetState(cleanup, "group_cleanup_unknown:"+c.NamespaceID, "1")
				}
				done()
			}
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		})
	}
	defer func() {
		if retErr != nil {
			release()
		}
	}()
	if err = m.Authorize(ctx, c); err != nil {
		return "", nil, release, err
	}
	stage = "configuration"
	cfg, ok := m.Config.Providers[base.Name()]
	if !ok || validateProviderRoutes(base.Name(), cfg.Routes) != nil {
		return "", nil, release, ErrUnavailable
	}
	catalogJSON, effectiveModel, catalogHash, err := m.qualifiedCatalog(base.Name(), cfg, selectedModel)
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	paths, err := runner.ValidateNamespacePaths(m.DataDir, c.Scope(), m.UID)
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	// Reconcile crashed-turn runtimes before new work; unknown removal denies.
	if err = m.reconcile(ctx, c.NamespaceID); err != nil {
		return "", nil, release, ErrUnavailable
	}
	if err = m.checkTrees(ctx, paths); err != nil {
		return "", nil, release, err
	}
	stage = "images"
	nativeImage, _, err := m.Docker.ImageInspectWithRaw(ctx, cfg.Image)
	if err != nil || nativeImage.Config == nil || nativeImage.Config.Labels["org.gopod.profile"] != "group" || nativeImage.Config.Labels["org.gopod.provider"] != base.Name() || nativeImage.Config.Labels["org.gopod.native-policy"] != runner.GroupNativePolicyVersion {
		return "", nil, release, ErrUnavailable
	}
	personalImage, _, err := m.Docker.ImageInspectWithRaw(ctx, base.Image())
	if err != nil || nativeImage.ID == personalImage.ID {
		return "", nil, release, ErrUnavailable
	}
	relayImage, _, err := m.Docker.ImageInspectWithRaw(ctx, m.Config.RelayImage)
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	toolImage, _, err := m.Docker.ImageInspectWithRaw(ctx, m.Config.ToolImage)
	if err != nil || toolImage.Config == nil || toolImage.Config.Labels["org.gopod.profile"] != "group-tools" || toolImage.Config.Labels["org.gopod.native-policy"] != revision || toolImage.ID == nativeImage.ID || toolImage.ID == personalImage.ID {
		return "", nil, release, ErrUnavailable
	}
	stage = "auth_ref"
	auth, err := m.authReference(cfg.AuthFile, paths.Policy)
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	cleanups = append(cleanups, func() {
		if !drainUnknown {
			_ = os.Remove(auth)
		}
	})
	leaseCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	cancelLease = cancel
	cleanups = append(cleanups, cancel)
	check := func(ctx context.Context) error {
		if leaseCtx.Err() != nil {
			return ErrUnavailable
		}
		return m.Authorize(ctx, c)
	}
	stage = "upload_import"
	if m.MCP.Executor == nil || m.MCP.Executor.ImportUploads(ctx, c.Folder, paths.Workspace, strings.ReplaceAll(GroupGuide, "CURRENT_GROUP_JID", c.JID), check) != nil {
		return "", nil, release, ErrUnavailable
	}
	stage = "grant"
	token, _, err := m.MCP.Grant(leaseCtx, groupsandbox.Lease{Scope: c.Folder, ToolImageID: toolImage.ID, Expires: time.Now().Add(30 * time.Minute), Validate: check, Request: func(ctx context.Context, args json.RawMessage) (string, error) {
		if check(ctx) != nil || m.Request == nil {
			return "", ErrUnavailable
		}
		return m.Request(ctx, c.Scope(), thread, args)
	}})
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	leaseToken = token
	prov, err = runner.GroupAgentProvider(base, runner.GroupProviderConfig{ModelCatalogJSON: catalogJSON, EffectiveModel: effectiveModel, Image: nativeImage.ID, MCPURL: "http://gopod-relay:8787/mcp/" + token})
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	policy := prov.(interface {
		PolicyFiles() map[string][]byte
		GroupExecEnv() []string
		ExpectedVersion() string
	})
	cert, key, err := isolationproxy.GenerateCA()
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	relayCfg := struct {
		Proxy  isolationproxy.Config
		MCPURL string
	}{Proxy: isolationproxy.Config{AllowClientToolSearch: base.Name() == "codex", RequireSubscriptionBearer: true, Token: token, Routes: cfg.Routes, RequiredTools: []string{"mcp__group__group_exec", "mcp__group__group_request"}, AllowedTools: []string{"mcp__group__group_exec", "mcp__group__group_request", "mcp__group.group_exec", "mcp__group.group_request", "group_exec", "group_request", "update_plan", "request_user_input", "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"}, CACertificatePEM: cert, CAPrivateKeyPEM: key}, MCPURL: "http://host.docker.internal:9877/mcp/" + token}
	raw, err := json.Marshal(relayCfg)
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	// Validate the exact policy now, before granting the native runtime any auth.
	verifier, err := isolationproxy.New(relayCfg.Proxy)
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	_ = verifier.Close()
	dir, err := os.MkdirTemp(paths.Policy, "turn-")
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	cleanups = append(cleanups, func() {
		if !drainUnknown {
			_ = os.RemoveAll(dir)
		}
	})
	relayFile := filepath.Join(dir, "relay.json")
	if err = os.WriteFile(relayFile, raw, 0600); err != nil {
		return "", nil, release, ErrUnavailable
	}
	caFile := filepath.Join(dir, "ca.crt")
	if err = os.WriteFile(caFile, cert, 0600); err != nil {
		return "", nil, release, ErrUnavailable
	}
	seed := make([]byte, 16)
	if _, err = rand.Read(seed); err != nil {
		return "", nil, release, ErrUnavailable
	}
	leaseID := hex.EncodeToString(seed)
	fingerprintBytes, _ := json.Marshal(struct {
		Revision, Qualification, Namespace string
		Epoch, Registration                int64
		Native, Relay, Tools, Provider     string
		CatalogHash, EffectiveModel        string
		Routes                             []isolationproxy.Route
	}{revision + runner.GroupNativePolicyVersion, m.Config.Qualification, c.NamespaceID, c.NamespaceEpoch, c.RegistrationRevision, nativeImage.ID, relayImage.ID, toolImage.ID, base.Name(), catalogHash, effectiveModel, cfg.Routes})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(fingerprintBytes))
	labels := map[string]string{"gopod.group-native": c.NamespaceID, "gopod.group-lease": leaseID, "gopod.group-policy": fingerprint, "gopod.version": m.Version}
	stage = "network"
	netCreated, err := m.Docker.NetworkCreate(ctx, "gopod-group-"+leaseID, network.CreateOptions{Driver: "bridge", Internal: true, EnableIPv6: boolPtr(false), Options: map[string]string{"com.docker.network.bridge.gateway_mode_ipv4": "isolated"}, Labels: labels})
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	cleanups = append(cleanups, func() {
		clean, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		if err := m.Docker.NetworkRemove(clean, netCreated.ID); err != nil {
			_ = m.Store.SetState(clean, "group_cleanup_unknown:"+c.NamespaceID, "1")
		}
	})
	netInfo, err := m.Docker.NetworkInspect(ctx, netCreated.ID, network.InspectOptions{})
	if err != nil || !netInfo.Internal || netInfo.Options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" || netInfo.EnableIPv6 {
		return "", nil, release, ErrUnavailable
	}
	// Relay is the only dual-homed container; it has no native credentials/socket.
	relayHost := safeHost()
	relayHost.Tmpfs["/home/node"] = fmt.Sprintf("rw,nosuid,nodev,size=16m,mode=0700,uid=%d,gid=%d", m.UID, m.GID)
	relayHost.ExtraHosts = []string{"host.docker.internal:host-gateway"}
	relayHost.Mounts = []mount.Mount{{Type: mount.TypeBind, Source: relayFile, Target: "/etc/gopod-group/relay.json", ReadOnly: true}}
	stage = "relay"
	relayID, err := m.create(ctx, "gopod-relay-"+leaseID, &container.Config{Image: relayImage.ID, User: m.user(), Entrypoint: []string{"/usr/local/bin/gopod-group-relay"}, Labels: labels, Env: []string{"PATH=/usr/local/bin:/usr/bin:/bin"}}, relayHost, nil)
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	cleanups = append(cleanups, m.remove(relayID, c.NamespaceID))
	if err = m.Docker.NetworkConnect(ctx, netCreated.ID, relayID, &network.EndpointSettings{Aliases: []string{"gopod-relay"}}); err != nil {
		return "", nil, release, ErrUnavailable
	}
	cliHost := safeHost()
	cliHost.Tmpfs["/home/node"] = fmt.Sprintf("rw,nosuid,nodev,size=16m,mode=0700,uid=%d,gid=%d", m.UID, m.GID)
	cliHost.NetworkMode = container.NetworkMode(netCreated.ID)
	cliHost.Mounts = []mount.Mount{{Type: mount.TypeBind, Source: caFile, Target: runner.GroupPolicyCA, ReadOnly: true}}
	state := filepath.Join(paths.Native, base.Name())
	stateTarget := "/home/node/." + base.Name()
	cliHost.Mounts = append(cliHost.Mounts, mount.Mount{Type: mount.TypeBind, Source: state, Target: stateTarget})
	authTarget := stateTarget + "/auth.json"
	if base.Name() == "claude" {
		authTarget = stateTarget + "/.credentials.json"
	}
	cliHost.Mounts = append(cliHost.Mounts, mount.Mount{Type: mount.TypeBind, Source: auth, Target: authTarget, ReadOnly: true})
	for target, data := range policy.PolicyFiles() {
		p := filepath.Join(dir, fmt.Sprintf("policy-%x", sha256.Sum256([]byte(target))))
		if err = os.WriteFile(p, data, 0600); err != nil {
			return "", nil, release, ErrUnavailable
		}
		cliHost.Mounts = append(cliHost.Mounts, mount.Mount{Type: mount.TypeBind, Source: p, Target: target, ReadOnly: true})
	}
	proxyURL := "http://group:" + token + "@gopod-relay:8787"
	env := append(policy.GroupExecEnv(), "HOME=/home/node", "PATH=/usr/local/bin:/usr/bin:/bin", "HTTPS_PROXY="+proxyURL, "HTTP_PROXY="+proxyURL, "https_proxy="+proxyURL, "http_proxy="+proxyURL, "NO_PROXY=gopod-relay", "no_proxy=gopod-relay")
	cliLabels := map[string]string{}
	for k, v := range labels {
		cliLabels[k] = v
	}
	cliLabels[runner.LabelChat] = c.Folder
	stage = "native_create"
	id, err = m.create(ctx, "gopod-native-"+leaseID, &container.Config{Image: nativeImage.ID, User: m.user(), WorkingDir: runner.GroupInferenceDir, Entrypoint: []string{"sleep", "infinity"}, Env: env, Labels: cliLabels}, cliHost, nil)
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	cleanups = append(cleanups, m.remove(id, c.NamespaceID))
	stage = "boundary_check"
	if err = m.verify(ctx, id, nativeImage.ID, fingerprint, netCreated.ID, cliHost); err != nil {
		return "", nil, release, ErrUnavailable
	}
	stage = "version_probe"
	version, err := m.exec(ctx, id, prov.VersionCmd())
	if err != nil || !exactVersion(version, policy.ExpectedVersion()) {
		return "", nil, release, ErrUnavailable
	}
	stage = "auth_probe"
	authStdout, authStderr, err := m.probe(ctx, id, prov.AuthStatusCmd())
	authOutput := authStdout
	if strings.TrimSpace(authOutput) == "" && base.Name() == "codex" {
		authOutput = authStderr
	}
	if err != nil {
		return "", nil, release, ErrUnavailable
	}
	status, err := prov.ParseAuthStatus(authOutput)
	if err != nil || !status.LoggedIn {
		return "", nil, release, ErrUnavailable
	}
	if base.Name() == "claude" && status.AuthMethod != "claude.ai" && status.AuthMethod != "oauth_token" {
		return "", nil, release, ErrUnavailable
	}
	if base.Name() == "codex" && status.AuthMethod != "chatgpt" {
		return "", nil, release, ErrUnavailable
	}
	return id, leasedProvider(prov, c.JID, check), release, nil
}

func boolPtr(v bool) *bool      { return &v }
func (m *Manager) user() string { return strconv.Itoa(m.UID) + ":" + strconv.Itoa(m.GID) }
func safeHost() *container.HostConfig {
	pids := int64(64)
	return &container.HostConfig{ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Resources: container.Resources{Memory: 768 << 20, NanoCPUs: 1000000000, PidsLimit: &pids, Ulimits: []*units.Ulimit{{Name: "fsize", Soft: 32 << 20, Hard: 32 << 20}}}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,noexec,size=32m,mode=1777", "/home/node": "rw,nosuid,nodev,size=16m,mode=0700"}}
}
func (m *Manager) create(ctx context.Context, name string, c *container.Config, h *container.HostConfig, n *network.NetworkingConfig) (string, error) {
	r, err := m.Docker.ContainerCreate(ctx, c, h, n, nil, name)
	if err != nil {
		return "", err
	}
	if err = m.Docker.ContainerStart(ctx, r.ID, container.StartOptions{}); err != nil {
		_ = m.Docker.ContainerRemove(context.WithoutCancel(ctx), r.ID, container.RemoveOptions{Force: true})
		return "", err
	}
	return r.ID, nil
}
func (m *Manager) remove(id, namespace string) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := m.Docker.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
			if m.Log != nil {
				m.Log.Warn("group runtime cleanup unverified")
			}
			_ = m.Store.SetState(ctx, "group_cleanup_unknown:"+namespace, "1")
		}
	}
}
func (m *Manager) exec(ctx context.Context, id string, cmd []string) (string, error) {
	out, _, err := m.probe(ctx, id, cmd)
	return out, err
}
func (m *Manager) probe(ctx context.Context, id string, cmd []string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	x, err := m.Docker.ContainerExecCreate(ctx, id, container.ExecOptions{Cmd: cmd, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", "", ErrUnavailable
	}
	a, err := m.Docker.ContainerExecAttach(ctx, x.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", ErrUnavailable
	}
	defer a.Close()
	stop := context.AfterFunc(ctx, func() { a.Close() })
	defer stop()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, io.LimitReader(a.Reader, 8192))
	state, inspectErr := m.Docker.ContainerExecInspect(ctx, x.ID)
	if inspectErr != nil || state.Running || state.ExitCode != 0 || err != nil {
		return "", "", ErrUnavailable
	}
	return stdout.String(), stderr.String(), nil
}

func exactVersion(output, expected string) bool {
	switch expected {
	case "2.1.310":
		return strings.TrimSpace(output) == "2.1.310 (Claude Code)"
	case "0.164.0":
		return strings.TrimSpace(output) == "codex-cli 0.164.0"
	default:
		return false
	}
}
