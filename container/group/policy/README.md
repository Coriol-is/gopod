# Pinned group model metadata

`codex-models-0.164.0.json` is a sanitized metadata-only snapshot derived from
OpenAI Codex's public catalog. It contains all 11 public source slugs, reduced
native capabilities and fixed neutral shared-group instructions. Original
vendor instruction templates and owner HOME/cache data are not included.

Source: [OpenAI Codex models catalog](https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/codex-rs/models-manager/models.json),
commit `6aaac694803004a63c6f393d153c96d22521a6b9` (`rust-v0.164.0`).
Upstream [LICENSE](LICENSE.codex) and [NOTICE](NOTICE.codex) are retained beside the artifact.
The source project is [Apache-2.0 licensed](https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/LICENSE).

- Raw public source SHA256: `943ca7fe1d19ed019054158303f0dbbd80b3bef43b3aa425a71aa7cb3fb52c2b`
- Sanitized artifact SHA256: `7718d30f843b20ae77c5209208ac77eff668bee5afefc02d9ae65b3934ee0091`
- Explicit fresh-group default: `gpt-6.1-sol`; private owner selection is unchanged.

Regenerate from a checkout containing the pinned upstream commit, then run the
catalog preparer from the gopod repository root:

```sh
git -C /path/to/codex show 6aaac694803004a63c6f393d153c96d22521a6b9:codex-rs/models-manager/models.json > /tmp/codex-0.164.0-public-models.json
go run ./cmd/gopod-group-catalog \
  --source /tmp/codex-0.164.0-public-models.json \
  --output container/group/policy/codex-models-0.164.0.json \
  --default-model gpt-6.1-sol
```

This file is not copied into native images. Deployment explicitly prepares
`DataDir/group-codex-catalog.json` from this artifact. The controller validates
and snapshots that host-selected catalog into each runtime as a read-only
policy mount; it pins the exact selected/default member in every CLI command.
Unknown model selections fail before native execution. No owner HOME, model
cache, credentials or account services are automatically imported.

The public source default follows ascending priority and first picker-visible
model. Availability in this snapshot does not prove account entitlement or
quota. Native inventory, transport and isolation acceptance are separate checks.

## Offline native regression

`TestGroupNativeInventoryPublicCatalog` runs all 11 exact catalog slugs through
Codex 0.164.0 with local mock provider/MCP endpoints and a fresh synthetic HOME.
Each model has five cases, each covering new and resumed sessions: scoped MCP
execution, hostile native exec, patch deletion of a synthetic auth canary, patch
creation of a native file, and hostile native file reading. The 55 cases assert
exact admitted model selection, scoped/audited inventory, unchanged canary and
absence of native file writes. Hidden catalog slugs receive the same checks.

```sh
GOPOD_GROUP_NATIVE_TEST=1 go test ./internal/runner -run '^TestGroupNativeInventory' -count=1
```

The same command includes the existing 14 synthetic hostile-metadata cases.
These are pinned CLI/catalog protocol and authority regressions; no account,
Telegram or real backend/model is used. They do not establish entitlement,
remaining quota or live inference availability for any listed slug.
