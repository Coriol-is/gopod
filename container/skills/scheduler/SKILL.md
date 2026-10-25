# Scheduling and recurring work

When the user asks for recurring tasks, reminders, scheduled reports, or task
management, read `/workspace/chat/SCHEDULING.md` first. Use the host's IPC task
schema and wait for a successful result receipt. Never guess a schedule format,
mutate SQLite, or claim that scheduling itself sends a Telegram notification.
Ask for the intended timezone when ambiguous. Cron can carry an explicit IANA
zone (`CRON_TZ=Europe/Belgrade ...`); intervals are elapsed durations.

The guide is embedded and seeded by gopod even for existing chat workspaces;
this optional skill mount is additional discoverability, not a requirement.
