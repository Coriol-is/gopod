#!/usr/bin/env python3
"""Disposable offline health qualification. Local base images only, no host mounts.

Requires Docker and Go. Images below are fixtures with installed provider payloads;
this does not claim a fresh release-image build or privileged host qualification.
Only this script's uniquely named containers/images/temp directory are removed.
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]

def call(*args, **kw):
    return subprocess.check_output(args, text=True, **kw).strip()

def main():
    suffix = uuid.uuid4().hex[:10]
    names, images = [], []
    def run(image, *args):
        name = 'gopod-health-' + suffix + '-' + str(len(names))
        names.append(name)
        call('docker', 'run', '-d', '--pull=never', '--name', name,
             '--network=none', '--read-only', '--cap-drop=ALL',
             '--security-opt=no-new-privileges', '--health-interval=1s',
             '--health-start-period=1s', '--health-timeout=3s', '--health-retries=2',
             *args, image)
        return name
    def expect(name, status):
        deadline = time.monotonic() + 35
        while time.monotonic() < deadline:
            state = json.loads(call('docker', 'inspect', '--format', '{{json .State}}', name))
            health = state.get('Health', {}).get('Status')
            if health == status:
                print(name, status, flush=True)
                return
            if not state['Running']:
                raise RuntimeError(f'{name} exited {state["ExitCode"]}')
            time.sleep(.5)
        raise RuntimeError(f'{name}: expected {status}, got {health}')
    try:
        with tempfile.TemporaryDirectory(prefix='gopod-health-') as tmp:
            tmp = Path(tmp)
            arch = call('docker', 'info', '--format', '{{.Architecture}}')
            arch = {'aarch64':'arm64', 'x86_64':'amd64'}.get(arch, arch)
            env = dict(os.environ, GOOS='linux', GOARCH=arch, CGO_ENABLED='0')
            for binary in ['gopod', 'gopod-group-relay']:
                subprocess.run(['go','build','-o',str(tmp/binary),'./cmd/'+binary],cwd=ROOT,env=env,check=True)
            (tmp/'healthcheck.sh').write_bytes((ROOT/'container/healthcheck.sh').read_bytes())
            def build(label, base, extra):
                call('docker','image','inspect',base,'--format','{{.Id}}') # no implicit pull
                tag = 'gopod-health-fixture:' + suffix + '-' + label
                (tmp/'Dockerfile').write_text('FROM '+base+'\nUSER 0:0\n'+extra+'\n')
                subprocess.run(['docker','build','--network=none','--pull=false','-q','-t',tag,str(tmp)],check=True,stdout=subprocess.DEVNULL)
                images.append(tag)
                return tag
            controller=build('controller',os.getenv('GOPOD_HEALTH_CONTROLLER_IMAGE','gopod-controller:8bdff46'),
                'COPY gopod /usr/local/bin/gopod\nENV GOPOD_HEALTH_ADDR=127.0.0.1:9878\nHEALTHCHECK CMD ["gopod","healthcheck"]\nENTRYPOINT ["gopod","-dotenv=false"]')
            controller_args=['--user=1000:1000','--tmpfs=/data:rw,mode=1777','-e','GOPOD_DATA_DIR=/data','-e','GOPOD_NO_CONTAINER=1']
            expect(run(controller,*controller_args),'healthy')
            expect(run(controller,*controller_args,'-e','GOPOD_RECOVERY_QUARANTINE=1'),'unhealthy')
            for provider in ['claude','codex']:
                base=os.getenv('GOPOD_HEALTH_'+provider.upper()+'_IMAGE', 'gopod-agent-elevated'+('-codex' if provider=='codex' else '')+':login-test')
                for profile,workspace,user in [('personal','/workspace/chat','1000:1000'),('group','/workspace/inference','1000:1000'),('elevated','/workspace/session','0:0')]:
                    tag=build(profile+'-'+provider,base,
                        'RUN mkdir -p '+workspace+' /etc/gopod-group && touch /etc/gopod-group/ca.crt\nCOPY healthcheck.sh /healthcheck.sh\nHEALTHCHECK CMD ["/bin/sh","/healthcheck.sh","'+provider+'","'+workspace+'"]\nENTRYPOINT ["sleep","infinity"]')
                    expect(run(tag,'--user='+user),'healthy')
                    if profile=='personal':
                        expect(run(tag,'--user='+user,'--health-cmd=/bin/sh /healthcheck.sh '+provider+' /missing-workspace'),'unhealthy')
            fixture=build('supervisor','gopod-elevated-supervisor:login-test',
                'COPY healthcheck.sh /healthcheck.sh\nHEALTHCHECK CMD ["/bin/sh","/healthcheck.sh","supervisor","/tmp"]\nENTRYPOINT ["sleep","infinity"]')
            expect(run(fixture,'--user=1000:1000'),'healthy')
            # Relay without config must fail closed, never become healthy merely
            # because a health binary exists. Positive serving loop is unit-tested.
            relay=build('relay',os.getenv('GOPOD_HEALTH_CONTROLLER_IMAGE','gopod-controller:8bdff46'),
                'COPY gopod-group-relay /usr/local/bin/gopod-group-relay\nHEALTHCHECK CMD ["gopod-group-relay","healthcheck"]\nENTRYPOINT ["gopod-group-relay"]')
            name=run(relay,'--user=1000:1000')
            code=call('docker','wait',name)
            if code!='1': raise RuntimeError('relay missing config did not fail closed')
            print('relay missing config: exit 1; no readiness',flush=True)
    finally:
        for name in names:
            subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        for tag in images:
            subprocess.run(['docker','image','rm',tag],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)

if __name__ == '__main__':
    main()
