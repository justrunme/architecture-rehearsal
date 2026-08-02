# Snapshot JSON (v0.1)

Human-readable schema notes. Formal JSON Schema can follow later.

## Snapshot

| Field | Type | Notes |
| ----- | ---- | ----- |
| id | string | stable id |
| name | string | display name |
| source | string | k8s-snapshot, prometheus-metadata, … |
| phase | string | baseline \| proposed \| deployed \| observed |
| createdAt | RFC3339 | |
| labels | object | free-form |
| meta | object | capacity, metric_labels, metrics |
| nodes[] | Node | |
| edges[] | Edge | |

## Node

| Field | Type |
| ----- | ---- |
| id | string (unique) |
| kind | Cluster, Node, Namespace, Workload, Service, PVC, Volume, Alert, SLO, Team, Change |
| name | string |
| namespace | string optional |
| attributes | object |

## Edge

| Field | Type |
| ----- | ---- |
| from | node id |
| to | node id |
| rel | DEPENDS_ON, RUNS_ON, BINDS_VOLUME, PROTECTED_BY, OBSERVED_BY, OWNED_BY, DEPLOYED_WITH, SCHEDULES_ON |
| attributes | object optional |

## Change envelope

| Field | Type |
| ----- | ---- |
| id, title, kind, description | string |
| seeds | node ids primarily affected |
| facts | scenario inputs (pods_requested, label_matchers, …) |
| addedNodes / removedNodes / patchNodes / addedEdges | graph deltas |
