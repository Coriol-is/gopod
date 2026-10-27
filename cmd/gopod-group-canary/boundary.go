package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/Coriol-is/gopod/internal/groupruntime"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/api/types/network"
)

var errBoundaryUnavailable = errors.New("native boundary unverified")

// Controller-only diagnostics never expose individual addresses, environment
// values or file contents. The native agent has no tool to invoke this script.
func nativeBoundaryPreflight(ctx context.Context, m *groupruntime.Manager, d *runner.Docker, c store.RegisteredChat, base runner.AgentProvider) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	id, _, release, err := m.Start(ctx, c, base, 0, "")
	if release != nil {
		defer release()
	}
	if err != nil {
		return errBoundaryUnavailable
	}
	bridge, err := d.Client().NetworkInspect(ctx, "bridge", network.InspectOptions{})
	if err != nil {
		return errBoundaryUnavailable
	}
	gateway := ""
	for _, config := range bridge.IPAM.Config {
		if ip := net.ParseIP(config.Gateway); ip != nil && ip.To4() != nil {
			if gateway != "" && gateway != ip.String() {
				return errBoundaryUnavailable
			}
			gateway = ip.String()
		}
	}
	return checkNativeBoundary(ctx, gateway, func(ctx context.Context, cmd []string) (*runner.ExecResult, error) { return d.Exec(ctx, id, cmd, nil) })
}
func checkNativeBoundary(ctx context.Context, gateway string, exec func(context.Context, []string) (*runner.ExecResult, error)) error {
	ip := net.ParseIP(gateway)
	if ip == nil || ip.To4() == nil {
		return errBoundaryUnavailable
	}
	res, err := exec(ctx, []string{"node", "-e", nativeBoundaryScript, ip.String()})
	if err != nil || res == nil || res.ExitCode != 0 || res.Stderr != "" {
		return errBoundaryUnavailable
	}
	var report struct {
		Verified bool `json:"boundary_verified"`
	}
	if json.Unmarshal([]byte(res.Stdout), &report) != nil || !report.Verified {
		return errBoundaryUnavailable
	}
	return nil
}

const nativeBoundaryScript = `
const fs=require('node:fs'),net=require('node:net');
const forbidden=['/workspace/vault','/workspace/project','/workspace/memory','/workspace/ipc','/var/run/docker.sock'];
const env=['GOPOD_MEMORY_CAPABILITY','OPENAI_API_KEY','ANTHROPIC_API_KEY','FREEFEED_PASSWORD','GOOGLE_APPLICATION_CREDENTIALS'];
function connect(host,port){return new Promise(resolve=>{let done=false;const s=net.connect({host,port});const finish=value=>{if(done)return;done=true;s.destroy();resolve(value)};s.setTimeout(2000,()=>finish(false));s.on('connect',()=>finish(true));s.on('error',()=>finish(false));});}
(async()=>{
 const noFiles=forbidden.every(path=>{try{fs.lstatSync(path);return false}catch(e){return e.code==='ENOENT'}});
 const noServices=env.every(key=>!Object.prototype.hasOwnProperty.call(process.env,key));
 const results=await Promise.all([connect('1.1.1.1',443),connect(process.argv[1],9876),connect(process.argv[1],9877),connect('2606:4700:4700::1111',443),connect('gopod-relay',8787)]);
 const ok=noFiles&&noServices&&results.slice(0,4).every(value=>!value)&&results[4];
 process.stdout.write(JSON.stringify({boundary_verified:ok}));process.exitCode=ok?0:1;
})().catch(()=>{process.stdout.write('{"boundary_verified":false}');process.exitCode=1;});
`
