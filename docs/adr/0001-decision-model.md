# ADR-0001: approve / warn / block / unknown

## Status

Accepted for Architecture Rehearsal v1.0.

## Context

A change gate that only returns approve/block will **false-approve** when the graph lacks required facts (no PVC list, no metric schema, partial RBAC).

## Decision

Four outcomes:

| Decision | Meaning |
| -------- | ------- |
| `approve` | No matched high-risk patterns; required coverage present |
| `warn` | Medium risk findings |
| `block` | High/critical findings with evidence |
| `unknown` | Required data missing — **not safe to approve** |

Exit codes: 0 / 1 / 3 / 4 respectively (2 = validation, 5 = internal).

## Rule

> Missing data never becomes false `approve`.

## Consequences

CI must treat exit 4 as a failing gate (or explicit allow_failure policy). Operators fix collectors/RBAC rather than ignoring unknown.
