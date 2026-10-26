# Offline group administration

This host-only command calls the same CP-020 Manager operations without Telegram,
listeners, scheduler, model inference or application environment loading. It
connects only to the local default Docker socket and never honors DOCKER_HOST.

Keep the known controller stopped for the entire operation. The command requires
an existing stopped compose `gopod` controller whose RW DataDir bind matches the
explicit host DataDir. Missing/mistyped names, unknown/running/restarting state
fail closed. A rooted no-follow private lock serializes concurrent admin commands;
it is not a replacement for stopping the controller or excluding other DB writers.
The existing store/config must be regular files beneath the protected DataDir.

Example, run as the non-root operator (UID/GID must both be positive):

```sh
gopod-group-admin --action status \
  --controller-name gopod-gopod-1 --jid tg:-1003850302017 \
  --data /opt/gopod/data --store /opt/gopod/data/store.sqlite \
  --config /opt/gopod/data/group-runtime.json
```

Use the observed registered revision for mutations:

```sh
gopod-group-admin --action prepare --expected-revision 1 \
  --controller-name gopod-gopod-1 --jid tg:-1003850302017 \
  --data /opt/gopod/data --store /opt/gopod/data/store.sqlite \
  --config /opt/gopod/data/group-runtime.json
```

`prepare` preserves retired data and pauses old schedules; it does not enable
inference. Inspect the new registration revision with status before a separate
`enable`. `disable` prevents new work without deleting historical data. All
operations use current scope checks and safe generic errors; no auth values are
printed. Enabling means configuration readiness, not a model round-trip check.
Store.Open may apply the current binary's normal additive schema initialization;
this command is not a raw read-only SQLite inspector. Back up/qualify the release
and keep the controller stopped before using it on production state.
