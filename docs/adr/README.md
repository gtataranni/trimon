# Architecture Decision Records

An ADR captures a single significant decision: the context that forced it, the choice we
made, and the consequences we accepted. ADRs record **why** — the *what* lives in the code
and the example config, which are the source of truth for current behaviour. Reference
docs link here instead of restating rationale inline, so a design decision has exactly one
home and cannot drift out of sync with prose.

## Writing a new ADR

1. Copy [0000-template.md](0000-template.md) to `NNNN-short-title.md` with the next number.
2. Fill in Context / Decision / Consequences. Keep it to what a future reader needs.
3. Set the status. When a later ADR overrides this one, mark this `superseded by` and link.
4. Link the ADR from the relevant reference doc (leave a one-line summary there).

New significant decisions land as an ADR, not as a new paragraph bolted onto a reference
doc.
