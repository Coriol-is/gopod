// Dedicated elevated exec supervisor. No PID file or host PID is a signal target.
// The inner supervisor owns a fresh process group and kills only its own group.
const {spawn} = require('node:child_process');
const fs = require('node:fs');
const args = process.argv.slice(2);
if (args[0] === '--inner') {
  const killOwnGroup = () => process.kill(0, 'SIGKILL');
  process.stdin.resume();
  process.stdin.on('end', killOwnGroup);
  process.stdin.on('error', killOwnGroup);
  const child = spawn(args[1], args.slice(2), {stdio: ['ignore', 'inherit', 'inherit']});
  const finish = code => { fs.writeSync(3, String(code)); killOwnGroup(); };
  child.on('error', () => finish(127));
  child.on('exit', code => finish(code === null ? 137 : code));
} else {
  const child = spawn(process.execPath, [__filename, '--inner', ...args], {
    detached: true, stdio: ['pipe', 'inherit', 'inherit', 'pipe']
  });
  let result = '';
  child.stdio[3].on('data', b => { result += b.toString(); });
  process.stdin.pipe(child.stdin);
  child.stdin.on('error', () => {});
  child.on('error', () => process.exit(127));
  child.on('close', () => process.exit(/^\d{1,3}$/.test(result) ? Number(result) : 137));
}
