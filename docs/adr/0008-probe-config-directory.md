# ADR-0008: Probe config is a directory of merged `*.yaml` files

- **Status:** accepted
- **Date:** 2026-08-10

## Context

[ADR-0007](0007-two-config-files.md) fixed `--probes` at a single YAML file. Once a
deployment monitors more than a handful of sites, one monolithic probe file becomes a
merge-conflict magnet: separate teams own separate probe sets, but every edit touches the
same file. Config-management tooling (Ansible, Helm, ConfigMaps) also emits fragments more
naturally than it edits a shared document.

The competing options were: a `--probes-dir` flag, glob patterns in `--probes`, an
`include:` directive inside the probe file, accepting either a file or a directory, or
requiring a directory outright. Adding a flag or a glob shifts the ambiguity onto the
operator; `include:` introduces a resolution order and a cycle problem for no benefit.
Accepting both a file and a directory keeps two config shapes — two sets of rules about
where `global:` may live, two fingerprinting schemes, two code paths — alive forever, for
a case a one-file directory already covers.

## Decision

- `--probes` names a **directory**, always. Pointing it at a plain file is an error. No
  new flag, no globs, no `include:`.
- **Probes only.** `--config` (ops) stays a single immutable file per ADR-0007.
- `*.yaml` files **directly inside** the directory are merged in lexical order.
  Non-recursive; dotfiles, subdirectories, and other extensions are skipped.
- `global:` is allowed **only** in the reserved file **`_global.yaml`**, which must not
  declare `probes:`. `global:` in any other file is an error. If `_global.yaml` is absent,
  the built-in defaults apply.
- A directory containing **zero** `*.yaml` files is an **error**, at startup and on reload.
- Probe names must be unique **across** all files; the error names both offending files.
- `Config.SHA256` hashes the sorted `(filename, content)` pairs, so adds, removes, and
  renames all change the fingerprint.

## Consequences

- Probe ownership can be split per team or per site without coordinating edits.
- Exactly one place defines defaults, so "which file won?" never arises — unlike a
  last-writer-wins merge of `global:` across fragments.
- Exactly one probe config shape exists, so `global:` placement, fingerprinting, and the
  load path each have one rule rather than two.
- Existing single-file deployments must move their probe file into a directory and lift
  `global:` into `_global.yaml`. This is a breaking change, taken before v1.0.
- An empty directory failing loudly prevents a botched deploy from silently reducing
  trimon to zero probes; on reload the daemon keeps its previous config.
- Reload stays all-or-nothing: any error in any file rejects the whole set.
- `GET /config` output shape is unchanged — it still returns the merged effective probe
  config as one document. It is no longer directly re-usable as a directory entry, since
  it carries both `global:` and `probes:`; it must be split across `_global.yaml` and a
  fragment to be fed back in. Provenance (`Config.ProbeFiles`) is logged, not served.
- Recursive walking, glob patterns, and filesystem watching remain out of scope.
