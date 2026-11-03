#!/usr/bin/env python3
"""Prepare or explicitly apply the Pi memory-controller boot option; never reboot."""
import argparse
import datetime
import os
from pathlib import Path
import shutil

parser = argparse.ArgumentParser()
parser.add_argument('--apply', action='store_true')
args = parser.parse_args()
path = Path('/boot/firmware/cmdline.txt')
original = path.read_text()
if len(original.strip().splitlines()) != 1:
    raise SystemExit('Refusing unexpected multiline cmdline')
if 'cgroup_enable=memory' in original.split():
    raise SystemExit('Memory enable option already present; no change')
updated = original.rstrip() + ' cgroup_enable=memory\n'
print('Proposed change: append cgroup_enable=memory to /boot/firmware/cmdline.txt')
if not args.apply:
    print('Dry run only; no files changed. Explicit owner authorization required for apply/reboot.')
    raise SystemExit(0)
backup = path.with_name('cmdline.txt.pre-memory-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
if backup.exists():
    raise SystemExit('Backup already exists; refusing overwrite')
shutil.copy2(path, backup)
with backup.open('rb') as saved:
    os.fsync(saved.fileno())
# Firmware partition is FAT; preserve the existing file instead of renaming metadata.
with path.open('w') as stream:
    stream.write(updated)
    stream.flush()
    os.fsync(stream.fileno())
if path.read_text() != updated:
    raise SystemExit('Read-back failed; restore retained backup before reboot')
print('Applied and verified. Backup:', backup)
print('No reboot performed. Reboot and post-boot verification are separate authorized steps.')
