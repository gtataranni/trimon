# ADR-0008: Probe config may be a directory of merged `*.yaml` files

- **Status:** accepted
- **Date:** 2026-08-10

## Context

[ADR-0007](0007-two-config-files.md) fixed `--probes` at a single YAML file. Once a
deployment monitors more than a handful of sites, one monolithic probe file becomes a
merge-conflict magnet: separate teams own separate probe sets, but every edit touches the
same file. Config-management tooling (Ansible, Helm, ConfigMaps) also emits fragments more
naturally than it edits a shared document.

The competing options were: a `--probes-dir` flag, glob patterns in `--probes`, an
`include:` directive inside the probe file, or making `--probes` accept either a file or a
directory. Adding a flag or a glob shifts the ambiguity onto the operator; `include:`
introduces a resolution order and a cycle problem for no benefit.

## Decision

- `--probes` accepts a **plain file or a directory**. trimon `os.Stat`s the path; a
  directory switches to merge mode. No new flag, no globs, no `include:`.
- **Probes only.** `--config` (ops) stays a single immutable file per ADR-0007.
- In directory mode, `*.yaml` files **directly inside** the directory are merged in lexical
  order. Non-recursive; dotfiles, subdirectories, and other extensions are skipped.
- `global:` is allowed **only** in the reserved file **`_global.yaml`**, which must not
  declare `probes:`. `global:` in any other file is an error. If `_global.yaml` is absent,
  the built-in defaults apply.
- A directory containing **zero** `*.yaml` files is an **error**, at startup and on reload.
- Probe names must be unique **across** all files; the error names both offending files.
- `Config.SHA256` in directory mode hashes the sorted `(filename, content)` pairs, so adds,
  removes, and renames all change the fingerprint. Single-file mode keeps the raw-bytes
  hash unchanged.

## Consequences

- Probe ownership can be split per team or per site without coordinating edits.
- Exactly one place defines defaults, so "which file won?" never arises — unlike a
  last-writer-wins merge of `global:` across fragments.
- An empty directory failing loudly prevents a botched deploy from silently reducing
  trimon to zero probes; on reload the daemon keeps its previous config.
- Reload stays all-or-nothing: any error in any file rejects the whole set.
- `GET /config` output shape is unchanged — it still returns one probe document, preserving
  ADR-0007's property that the response is valid probe config input. Provenance
  (`Config.ProbeFiles`) is logged, not served.
- Recursive walking, glob patterns, and filesystem watching remain out of scope.
