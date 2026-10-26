"""Pinned native ChatGPT-auth SSE qualification with synthetic tokens only.

Both mock backend and CLI are confined to a disposable Docker internal network.
No real account file is inspected, copied or used. Images must be preloaded.
"""
import argparse
import base64,datetime,json,pathlib,subprocess,tempfile,uuid
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--cli-image',default='gopod-agent-group-codex:latest')
parser.add_argument('--mock-image',default='node:22-slim')
options=parser.parse_args()
version=subprocess.run(['docker','run','--rm','--pull=never','--network=none','--entrypoint=codex',options.cli_image,'--version'],text=True,capture_output=True,timeout=15)
if version.returncode or version.stdout.strip()!='codex-cli 0.164.0':
 raise SystemExit('pinned Codex 0.164.0 image required')
name='gopod-codex-fallback-'+uuid.uuid4().hex[:10]
network=name+'-net'; model=name+'-model'; cli=name+'-cli'
def run(args,**kw):return subprocess.run(args,check=True,text=True,capture_output=True,timeout=kw.pop('timeout',40),**kw)
def b64(value):return base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip('=')
js='''const http=require('http');
const event=(k,r)=>console.log(JSON.stringify({kind:k,path:r.url,bearer:!!r.headers.authorization,account:!!r.headers['chatgpt-account-id']}));
const srv=http.createServer((r,w)=>{event('http-'+r.method,r);if(r.method==='GET'){w.setHeader('Content-Type','application/json');if(r.url.startsWith('/backend-api/codex/models?'))w.end(JSON.stringify({models:[]}));else{w.writeHead(403);w.end(JSON.stringify({error:'denied'}));}return;}if(!r.url.endsWith('/backend-api/codex/responses')){r.resume();w.writeHead(403);w.end('denied');return;}let b='';r.on('data',x=>b+=x);r.on('end',()=>{const req=JSON.parse(b); console.log(JSON.stringify({kind:'request-fields',keys:Object.keys(req).sort()}));const response={id:'resp_fixture',object:'response',status:'completed',model:'gpt-5',output:[{id:'msg_fixture',type:'message',role:'assistant',status:'completed',content:[{type:'output_text',text:'synthetic-only',annotations:[]}]}],usage:{input_tokens:1,output_tokens:1,total_tokens:2}};w.writeHead(200,{'Content-Type':'text/event-stream'});for(const e of [{type:'response.created',response:{...response,status:'in_progress'}},{type:'response.output_item.added',output_index:0,item:response.output[0]},{type:'response.output_text.delta',item_id:'msg_fixture',output_index:0,content_index:0,delta:'synthetic-only'},{type:'response.output_item.done',output_index:0,item:response.output[0]},{type:'response.completed',response}])w.write('data: '+JSON.stringify(e)+'\\n\\n');w.end();});});srv.on('upgrade',(r,s)=>{event('websocket-426',r);s.end('HTTP/1.1 426 Upgrade Required\\r\\nConnection: close\\r\\nContent-Length: 0\\r\\n\\r\\n');});srv.listen(8080,'0.0.0.0');'''
with tempfile.TemporaryDirectory(prefix='gopod-codex-fallback-') as tmp:
 p=pathlib.Path(tmp);(p/'server.js').write_text(js)
 jwt=b64({'alg':'none'})+'.'+b64({'exp':4102444800,'email':'synthetic@example.invalid','https://api.openai.com/auth':{'chatgpt_plan_type':'pro','chatgpt_account_id':'synthetic-account','chatgpt_user_id':'synthetic-user'}})+'.synthetic'
 (p/'auth.json').write_text(json.dumps({'auth_mode':'chatgpt','OPENAI_API_KEY':None,'tokens':{'id_token':jwt,'access_token':jwt,'refresh_token':'synthetic-refresh-not-real','account_id':'synthetic-account'},'last_refresh':datetime.datetime.now(datetime.timezone.utc).isoformat()}))
 cfg=['model="gpt-5"','approval_policy="never"','sandbox_mode="read-only"','web_search="disabled"','check_for_update_on_startup=false','model_provider="gopod_group_native"',f'chatgpt_base_url="http://{model}:8080"','[features]']
 cfg += [key+'=false' for key in ['goals','shell_tool','shell_snapshot','unified_exec','apps','plugins','remote_plugin','plugin_sharing','hooks','multi_agent','multi_agent_v2','skill_mcp_dependency_install','skill_search','browser_use','browser_use_external','computer_use','image_generation','view_image']]
 cfg += ['[model_providers.gopod_group_native]','name="Group native ChatGPT"',f'base_url="http://{model}:8080/backend-api/codex"','requires_openai_auth=true','wire_api="responses"','supports_websockets=false']
 (p/'config.toml').write_text('\n'.join(cfg)+'\n')
 for f in p.iterdir():f.chmod(0o644)
 p.chmod(0o755)
 try:
  run(['docker','network','create','--internal',network])
  run(['docker','run','-d','--pull=never','--pids-limit=64','--memory=128m','--cpus=0.5','--name',model,'--network',network,'--network-alias',model,'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges','--user','1000:1000','--mount',f'type=bind,src={p}/server.js,dst=/server.js,readonly',options.mock_image,'node','/server.js'])
  args=['docker','run','--rm','--pull=never','--memory=768m','--name',cli,'--network',network,'--read-only','--cap-drop=ALL','--security-opt=no-new-privileges','--user','1000:1000','--pids-limit','128','--cpus','1','--tmpfs','/tmp:rw,nosuid,nodev,size=64m,mode=1777','--tmpfs','/home/node:rw,nosuid,nodev,size=64m,uid=1000,gid=1000,mode=700','--tmpfs','/home/node/.codex:rw,nosuid,nodev,size=64m,uid=1000,gid=1000,mode=700','--tmpfs','/workspace/inference:rw,nosuid,nodev,size=16m,uid=1000,gid=1000,mode=700','--workdir','/workspace/inference','--mount',f'type=bind,src={p}/auth.json,dst=/home/node/.codex/auth.json,readonly','--mount',f'type=bind,src={p}/config.toml,dst=/home/node/.codex/config.toml,readonly','--env','HOME=/home/node','--env','CODEX_HOME=/home/node/.codex','--env','CI=1','--entrypoint','codex',options.cli_image,'exec','--strict-config','--skip-git-repo-check','--ephemeral','--json','Reply synthetic-only; do not call tools.']
  result=subprocess.run(args,text=True,capture_output=True,stdin=subprocess.DEVNULL,timeout=40)
  events=[]
  for line in run(['docker','logs',model]).stdout.splitlines():
   try:events.append(json.loads(line))
   except ValueError:pass
  requests=[e for e in events if e.get('kind')=='http-POST' and e.get('path')=='/backend-api/codex/responses']
  websocket_attempts=sum(e.get('kind')=='websocket-426' for e in events)
  passed=result.returncode==0 and len(requests)==1 and websocket_attempts==0 and requests[0]['bearer'] and requests[0]['account']
  print(json.dumps({'passed':passed,'exit':result.returncode,'native_auth':'synthetic_chatgpt','internet':'internal_network_only','response_requests':len(requests),'websocket_attempts':websocket_attempts,'events':events}))
  if not passed:raise SystemExit(1)
 finally:
  subprocess.run(['docker','rm','-f',cli,model],capture_output=True,timeout=10)
  subprocess.run(['docker','network','rm',network],capture_output=True,timeout=10)
