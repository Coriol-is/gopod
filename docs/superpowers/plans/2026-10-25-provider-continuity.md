# Provider continuity implementation plan

1. Add the provider-neutral SQLite transcript, scoped legacy import, bounded
   JSON history, transactional completion/clear/provider persistence.
2. Hold the per-chat agent slot through ensure and execution on all runner
   paths. Replace destructive provider switching with waiting, checked
   container cleanup and persistence. Attach historical data to user prompts.
3. Update Router command wording and wiring, keeping confirmation/permissions.
4. Verify main/thread/chat isolation, round-trip switching, restart, failed
   persistence, legacy migration, quotas, clear, bounds and active-run waits.
5. Update D030, CP registry, ROADMAP and HANDOFF. Run build, vet, full tests
   and targeted race tests; independent review, PR and green CI before merge.

Automatic quota fallback and live deployment are explicitly deferred.
