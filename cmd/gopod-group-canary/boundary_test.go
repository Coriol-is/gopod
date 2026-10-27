package main

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner"
)

func TestBoundaryProbeUsesFixedScriptAndOnlyBooleanReport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *runner.ExecResult
		err    error
		deny   bool
	}{
		{"verified", &runner.ExecResult{Stdout: `{"boundary_verified":true}`}, nil, false},
		{"unverified", &runner.ExecResult{Stdout: `{"boundary_verified":false}`}, nil, true},
		{"native failure", nil, errors.New("PRIVATE_AUTH_DATA"), true},
		{"stderr", &runner.ExecResult{Stdout: `{"boundary_verified":true}`, Stderr: "PRIVATE_AUTH_DATA"}, nil, true},
		{"exit", &runner.ExecResult{Stdout: `{"boundary_verified":true}`, ExitCode: 1}, nil, true},
		{"invalid", &runner.ExecResult{Stdout: "PRIVATE_AUTH_DATA"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := checkNativeBoundary(t.Context(), "172.17.0.1", func(ctx context.Context, cmd []string) (*runner.ExecResult, error) {
				called = true
				if len(cmd) != 4 || cmd[0] != "node" || cmd[1] != "-e" || cmd[2] != nativeBoundaryScript || cmd[3] != "172.17.0.1" {
					t.Fatal("untrusted boundary invocation")
				}
				return tc.result, tc.err
			})
			if !called || (err != nil) != tc.deny {
				t.Fatalf("unexpected result %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("diagnostic disclosed native output")
			}
		})
	}
	called := false
	if checkNativeBoundary(t.Context(), "attacker.example", func(context.Context, []string) (*runner.ExecResult, error) { called = true; return nil, nil }) == nil || called {
		t.Fatal("non-inspected gateway accepted")
	}
}
func TestBoundaryScriptCoversPrivateMountsServicesAndNetworkControls(t *testing.T) {
	for _, required := range []string{"/workspace/vault", "/workspace/project", "/workspace/memory", "/workspace/ipc", "/var/run/docker.sock", "GOPOD_MEMORY_CAPABILITY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "FREEFEED_PASSWORD", "GOOGLE_APPLICATION_CREDENTIALS", "connect('1.1.1.1',443)", "connect(process.argv[1],9876)", "connect(process.argv[1],9877)", "connect('2606:4700:4700::1111',443)", "connect('gopod-relay',8787)", "setTimeout(2000"} {
		if !strings.Contains(nativeBoundaryScript, required) {
			t.Fatalf("missing boundary check %s", required)
		}
	}
	if strings.Contains(nativeBoundaryScript, "console.") || strings.Contains(nativeBoundaryScript, "JSON.stringify(process.env)") {
		t.Fatal("raw diagnostics enabled")
	}
}

func TestBoundaryNodeScriptWithMockedFilesEnvAndTCP(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable for diagnostic script qualification")
	}
	for _, scenario := range []string{"isolated", "public_egress", "host_egress", "ipv6_egress", "relay_absent", "private_mount", "personal_env"} {
		t.Run(scenario, func(t *testing.T) {
			harness := `const vm=require('node:vm'),EventEmitter=require('node:events');
 const scenario=` + strconv.Quote(scenario) + `;
 const mockProcess={argv:['node','172.17.0.1'],env:scenario==='personal_env'?{OPENAI_API_KEY:'PRIVATE_SECRET'}:{},stdout:{write:value=>process.stdout.write(value)},exitCode:0};
 const mockFS={lstatSync:path=>{if(scenario==='private_mount'&&path==='/workspace/vault')return {};const e=new Error('PRIVATE_PATH');e.code='ENOENT';throw e;}};
 const mockNet={connect:({host,port})=>{const s=new EventEmitter();s.setTimeout=()=>s;s.destroy=()=>{};let allowed=host==='gopod-relay'&&scenario!=='relay_absent';if(scenario==='public_egress'&&host==='1.1.1.1')allowed=true;if(scenario==='ipv6_egress'&&host.includes(':'))allowed=true;if(scenario==='host_egress'&&port===9877)allowed=true;queueMicrotask(()=>s.emit(allowed?'connect':'error',new Error('PRIVATE_NETWORK')));return s;}};
 vm.runInNewContext(` + strconv.Quote(nativeBoundaryScript) + `,{require:name=>name==='node:fs'?mockFS:mockNet,process:mockProcess});`
			out, err := exec.CommandContext(t.Context(), node, "-e", harness).CombinedOutput()
			if err != nil {
				t.Fatalf("mock script execution failed: %v", err)
			}
			want := `{"boundary_verified":false}`
			if scenario == "isolated" {
				want = `{"boundary_verified":true}`
			}
			if string(out) != want {
				t.Fatalf("unexpected fixed report %q", out)
			}
		})
	}
}
