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
  const interactive = args[1] === '--interactive';
  const command = interactive ? args.slice(2) : args.slice(1);
  const child = spawn(command[0], command.slice(1), {stdio: [interactive ? 'pipe' : 'ignore', 'inherit', 'inherit']});
  if (interactive) {
    process.stdin.pipe(child.stdin);
    child.stdin.on('error', () => {});
  }
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
