# Root broker validator fuzz checkpoint — 2026-10-24

## Scope

The root broker request and relative-path validators were fuzzed locally at
source revision [`ee40d2e`](https://github.com/cyberducttape/StePanel/commit/ee40d2eefc6ca6b217750da3e4bae91efa20a9ca).
The run used Go 1.26.10 on Linux x86_64, with two fuzz workers per target.

Commands:

```sh
go test ./internal/rootbroker -run '^$' -fuzz '^FuzzValidateRequest$' -fuzztime=30s -parallel=2
go test ./internal/rootbroker -run '^$' -fuzz '^FuzzValidateFilePath$' -fuzztime=30s -parallel=2
```

## Results

- `FuzzValidateRequest`: passed; 240,950 executions, 13 new interesting inputs
  in the Go fuzz cache, no panic or violated accepted-site invariant.
- `FuzzValidateFilePath`: passed; 244,529 executions, 6 new interesting inputs
  in the Go fuzz cache, no accepted absolute/traversing path or containment
  invariant failure.
- No failure corpus was written to the repository.

## Limits

This is a short local fuzz checkpoint, not exhaustive fuzzing or an independent
security assessment. It checks JSON request validation and lexical relative
path containment; it does not exercise filesystem symlink races, privileged
host mutations, service outages, or installed-host recovery. The production
security and recovery gates remain open.
