package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGroupAuthStatusUsesPinnedManagedPolicy(t *testing.T) {
	claude := groupFixture(t, "claude")
	cmd := claude.AuthStatusCmd()
	for _, flag := range []string{"--restricted", "--tools", "--strict-mcp-config", "--settings", "--setting-sources", "--disable-slash-commands"} {
		if !slices.Contains(cmd, flag) {
			t.Errorf("Claude auth missing immutable policy flag %s", flag)
		}
	}
	if i := slices.Index(cmd, "--tools"); i < 0 || cmd[i+1] != "" {
		t.Fatal("Claude auth enables builtins")
	}
	if slices.Contains(cmd, "--bare") {
		t.Fatal("group auth disabled subscription auth")
	}
	if !slices.Equal(cmd[len(cmd)-3:], []string{"auth", "status", "--json"}) {
		t.Fatal("Claude local auth subcommand not selected")
	}
	// Codex0.164 rejects --strict-config for login; login/status is its local
	// config/auth read, not an inference command that initializes MCP/tools.
	if got := groupFixture(t, "codex").AuthStatusCmd(); !slices.Equal(got, []string{"codex", "login", "status"}) {
		t.Fatal("unsupported Codex auth flags introduced")
	}
}

// This opt-in qualifies actual pinned group images, with fake auth and no
// network. No operator auth file or model endpoint is used. CLI images must be
// preloaded; docker never pulls during the fixture.
func TestGroupNativeAuthStatusOfflineDocker(t *testing.T) {
	if os.Getenv("GOPOD_GROUP_AUTH_IMAGE_TEST") != "1" {
		t.Skip("set GOPOD_GROUP_AUTH_IMAGE_TEST=1 with qualified group images")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			envName := "GOPOD_GROUP_" + strings.ToUpper(provider) + "_AUTH_IMAGE"
			image := os.Getenv(envName)
			if image == "" {
				image = map[string]string{"claude": "gopod-agent-group:latest", "codex": "gopod-agent-group-codex:latest"}[provider]
			}
			policy := groupFixture(t, provider)
			native := policy.(interface{ PolicyFiles() map[string][]byte })
			fixture := t.TempDir()
			if err := os.Chmod(fixture, 0755); err != nil {
				t.Fatal(err)
			}
			write := func(name string, data []byte) string {
				path := filepath.Join(fixture, name)
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
				return path
			}
			files := native.PolicyFiles()
			authTarget := "/home/node/.claude/.credentials.json"
			var auth []byte
			if provider == "claude" {
				auth, _ = json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-not-an-account-token", "refreshToken": "synthetic-not-a-refresh-token", "expiresAt": int64(4102444800000), "scopes": []string{"user:inference", "user:profile"}, "subscriptionType": "max"}})
			} else {
				authTarget = "/home/node/.codex/auth.json"
				encode := func(value any) string { b, _ := json.Marshal(value); return base64.RawURLEncoding.EncodeToString(b) }
				token := encode(map[string]string{"alg": "none"}) + "." + encode(map[string]any{"exp": int64(4102444800), "email": "synthetic@example.invalid", "https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": "pro", "chatgpt_account_id": "synthetic-account"}}) + ".synthetic"
				auth, _ = json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]any{"id_token": token, "access_token": token, "refresh_token": "synthetic-not-real", "account_id": "synthetic-account"}, "last_refresh": time.Now().UTC().Format(time.RFC3339)})
			}
			authPath := write("synthetic-auth.json", auth)
			name := fmt.Sprintf("gopod-auth-policy-%s-%d", provider, time.Now().UnixNano())
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
			})
			args := []string{"run", "--rm", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=1000:1000", "--pids-limit=128", "--memory=768m", "--cpus=1", "--tmpfs=/tmp:rw,nosuid,nodev,size=32m,mode=1777", "--tmpfs=/home/node:rw,nosuid,nodev,size=64m,uid=1000,gid=1000,mode=700", "--tmpfs=/home/node/.claude:rw,nosuid,nodev,size=32m,uid=1000,gid=1000,mode=700", "--tmpfs=/home/node/.codex:rw,nosuid,nodev,size=32m,uid=1000,gid=1000,mode=700", "--env=HOME=/home/node", "--env=CODEX_HOME=/home/node/.codex", "--env=CI=1", "--mount", "type=bind,src=" + authPath + ",dst=" + authTarget + ",readonly"}
			for target, data := range files {
				path := write(filepath.Base(target), data)
				args = append(args, "--mount", "type=bind,src="+path+",dst="+target+",readonly")
			}
			// Fixed image + policy env carries no API-key fallback. A missing MCP server
			// cannot be reached on network=none; auth status must remain a local read.
			cmd := policy.AuthStatusCmd()
			args = append(args, "--entrypoint", cmd[0], image)
			args = append(args, cmd[1:]...)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			var output []byte
			var err error
			if provider == "claude" {
				output, err = exec.CommandContext(ctx, "docker", args...).Output()
			} else {
				output, err = exec.CommandContext(ctx, "docker", args...).CombinedOutput()
			}
			if err != nil {
				t.Fatalf("offline managed auth status failed: %v (output bytes=%d)", err, len(output))
			}
			status, err := policy.ParseAuthStatus(string(output))
			if err != nil || !status.LoggedIn {
				t.Fatalf("synthetic native auth not recognized: %v", err)
			}
			if provider == "codex" && status.AuthMethod != "chatgpt" {
				t.Fatal("synthetic ChatGPT auth selected other billing method")
			}
			if provider == "claude" && status.AuthMethod != "claude.ai" && status.AuthMethod != "oauth_token" {
				t.Fatal("synthetic Claude native auth selected other billing method")
			}
			after, err := os.ReadFile(authPath)
			if err != nil || string(after) != string(auth) {
				t.Fatal("auth status changed read-only credential reference")
			}
		})
	}
}
