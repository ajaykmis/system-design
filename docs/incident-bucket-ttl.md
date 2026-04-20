# Production Incident: GCS Lifecycle Policy Deletes ANN Streaming Config

## Context

The retrieval-ann-service serves approximate nearest neighbor (ANN) search for Snapchat's content discovery pipeline. It reads HNSW indexes and embeddings from a GCS bucket at boot time.

### The Architecture

```
Story Posting → Kafka Ingestion → Embedding Generation (Indexing Stage)
                                         │
                                         ▼
                                   HNSW Index Files
                                   (stored in GCS bucket)
                                         │
                                         ▼
                                  retrieval-ann-service
                                  (reads indexes at boot)
```

The GCS bucket had a lifecycle policy to clean up old index files — indexes get rebuilt periodically, so old versions are safe to delete after N days. This had worked fine for years because the bucket only contained index files.

## What Changed: Streaming ANN Support

A new feature added streaming support for ANN — instead of only reading batch-built HNSW indexes, the ann-service could also consume embeddings in real-time from Kafka, improving content freshness.

```
BEFORE (batch only):
  Indexing stage → builds HNSW index → writes to GCS
  ann-service → reads index from GCS at boot → serves queries

AFTER (batch + streaming):
  Indexing stage → builds HNSW index → writes to GCS
                 → publishes embeddings to Kafka ──┐
                                                    │
  ann-service → reads index from GCS at boot        │
              → reads streaming config (CSV) from GCS
              → consumes from Kafka topics/partitions ◄┘
              → merges streaming results with batch index

New pipeline:
  story-posting → kafka ingestion → embedding generation
       → kafka publishing → ann-service consumes from kafka topics/partitions
```

The streaming config was a CSV file stored in the same GCS bucket as the indexes. It specified which indexes were running in streaming mode and the corresponding Kafka topics to consume from.

```
GCS bucket (before the feature):
  └── indexes/
        ├── hnsw_shard_0.bin
        ├── hnsw_shard_1.bin
        └── embeddings_v42.bin

GCS bucket (after the feature):
  └── indexes/
        ├── hnsw_shard_0.bin
        ├── hnsw_shard_1.bin
        ├── embeddings_v42.bin
        └── streaming_config.csv     ← NEW: which indexes stream from Kafka
```

## The Feature Launch

- A/B test showed significant improvement in content freshness
- Streaming ANN was rolled out to production
- Everything worked perfectly

## The Incident (~1 Month Later)

```
Timeline:

  Day 0:      Streaming config CSV written to bucket
              ann-service boots, reads config, connects to Kafka
              Everything works.

  Day 1-30:   Feature running perfectly in production.
              A/B metrics positive. No issues.

  Day ~30:    GCS lifecycle policy fires.
              Policy: "delete objects older than N days"
              The CSV is now older than N days.
              GCS silently deletes streaming_config.csv.
              No alert. No notification.

  Minutes later:
              ann-service pods reload (periodic config refresh).
              The reload logic reads config from GCS at boot AND
              on a reload cycle to pick up changes.
              File is gone. Pods fail to read config.
              Pods start crashing.

              ┌─────────────────────────────────┐
              │  ann-service reload cycle:       │
              │                                  │
              │  1. Read streaming_config.csv    │
              │     from GCS                     │
              │     → 404 Not Found              │
              │                                  │
              │  2. Pod fails / restarts          │
              │                                  │
              │  3. Boot sequence reads config   │
              │     → 404 Not Found again        │
              │                                  │
              │  4. Pod crash loop                │
              │  └──────────────────────────────┘│
              └─────────────────────────────────┘

  Detection:  ann-service pods crash-looping.
              Content retrieval degraded.
              On-call paged.

  Debugging:  "The config file just isn't there?"
              Check recent deploys — none.
              Check permissions — unchanged.
              Check GCS audit logs — lifecycle policy deleted it.
              Root cause identified.
```

## Why It Happened

```
1. SHARED BUCKET, DIFFERENT DATA LIFECYCLES
   ─ Index files: rebuilt regularly, safe to delete old versions
   ─ Config CSV: written once, must persist indefinitely
   ─ Both in the same bucket with the same lifecycle rule

2. LIFECYCLE POLICY WAS INVISIBLE
   ─ Set long before the streaming feature existed
   ─ Nobody checked bucket policies when adding the CSV
   ─ The feature launch checklist didn't include "verify storage policies"

3. DELAYED BLAST
   ─ Feature worked perfectly for a month
   ─ The lifecycle TTL was the time bomb
   ─ No connection between "launch day" and "failure day"
   ─ Makes it extremely hard to correlate

4. RELOAD LOGIC AMPLIFIED THE FAILURE
   ─ Pods had periodic config reload (good for picking up changes)
   ─ But reload treated missing file as fatal (bad)
   ─ Crash → restart → read config → still missing → crash loop
```

## The Fix

The config files were moved out of the index bucket (or an exception was added for config/ paths in the lifecycle policy — the exact remediation was one of these).

```
Option A: Separate buckets (cleanest):
  gs://snap-ann-indexes/          ← lifecycle policy: delete after N days
    ├── hnsw_shard_0.bin
    └── embeddings_v42.bin

  gs://snap-ann-config/           ← NO lifecycle policy
    └── streaming_config.csv

Option B: Exception in lifecycle rule:
  lifecycle:
    rule:
      - action: { type: Delete }
        condition:
          age: 30
          matchesPrefix: ["indexes/"]    ← only match index files
          # streaming_config.csv is NOT under indexes/, so it's safe
```

## Design Lessons

### 1. Different Data Lifecycles = Different Storage Boundaries

```
WRONG: Mix ephemeral and persistent data in the same bucket
       and rely on naming conventions to protect persistent data.

RIGHT: Separate by lifecycle. If data has different TTL
       expectations, it goes in different buckets/namespaces.
       A blanket policy can never accidentally cross the boundary.
```

### 2. Silent Destructive Processes Need Auditing

```
Lifecycle policies, cron jobs, TTL reapers — any background
process that deletes data should:
  ─ Log what it deletes (audit trail)
  ─ Emit metrics on deletion count (spike = something wrong)
  ─ Have a dry-run mode before production
  ─ Be reviewed when the bucket's usage changes
```

### 3. Config Read Failures Should Degrade, Not Crash

```
WRONG (what happened):
  config, err := readFromGCS("streaming_config.csv")
  if err != nil {
      log.Fatal(err)  // pod dies
  }

RIGHT (graceful degradation):
  config, err := readFromGCS("streaming_config.csv")
  if err != nil {
      log.Error("streaming config unavailable, falling back to batch-only mode")
      alert("streaming_config.csv missing from GCS — check lifecycle policies")
      // Continue serving with batch indexes only
      // Streaming is degraded but service stays up
  }
```

### 4. Feature Launch Checklists Should Include Storage Policies

```
Before launching a feature that writes to an existing bucket:
  □ Check lifecycle policies on the bucket
  □ Check retention policies
  □ Verify the new file's lifecycle matches the policy
  □ If different → separate bucket or add exception
  □ Add monitoring for unexpected deletions of the new file
```

## Interview Answer (Structured)

> "Tell me about a production incident you were involved in."

### Situation

I was on the Retrieval team at Snapchat. We own the retrieval-ann-service — it serves approximate nearest neighbor search for Spotlight content discovery. The service reads HNSW indexes from a GCS bucket at boot time, and we'd just shipped a new feature: streaming ANN support.

The idea was straightforward — instead of only serving from batch-built indexes (which could be hours stale), the ann-service would also consume real-time embeddings from Kafka, so newly posted content would be searchable within seconds instead of waiting for the next index rebuild. This was a big win for content freshness.

To configure which indexes should run in streaming mode and which Kafka topics to consume from, we stored a CSV config file in the same GCS bucket as the HNSW indexes. The feature A/B tested very well — significant improvement in content freshness metrics — and we rolled it out to production. Everything worked perfectly.

### What Went Wrong

About a month after the launch, the ann-service pods started crash-looping. Content retrieval was degraded. On-call got paged.

When we investigated, the streaming config CSV file was just gone from the bucket. No recent deploys, no permission changes, no one had deleted it manually. We checked GCS audit logs and found the root cause: a lifecycle policy on the bucket had silently deleted it.

The bucket had a lifecycle rule — set years before the streaming feature existed — that deleted objects older than N days. This made perfect sense for index files, which get rebuilt regularly. Old index versions are safe to clean up. But nobody checked the bucket's lifecycle policies when we added the config file. The CSV was written once and never updated, so after N days, the lifecycle policy treated it like a stale index and deleted it.

The failure was amplified because the ann-service had a periodic config reload cycle — it re-read the config from GCS to pick up changes. When the file disappeared, the reload failed, and the service treated a missing config as fatal. Pods crashed, restarted, tried to read the config again, got a 404 again, and entered a crash loop.

### What Made It Hard

Three things made this particularly difficult:

First, the **delayed blast**. The feature worked perfectly for a month. There was no connection between launch day and failure day. When pods started crashing, nobody thought "maybe something changed in the bucket" because nothing had been deployed or modified recently.

Second, the **silent deletion**. GCS lifecycle policies run as background jobs with no alerts or notifications. The file just stopped existing. The audit log showed it, but we didn't have monitoring on lifecycle deletions.

Third, the **crash loop amplification**. A missing config should have been a degraded-mode situation (fall back to batch-only), not a fatal crash. The reload logic was designed to pick up config changes, which is good, but it treated any read failure as unrecoverable, which turned a data issue into a full service outage.

### How We Fixed It

Immediate fix: we restored the config file and pods recovered. For the permanent fix, we separated the config files from the index bucket so the lifecycle policy couldn't reach them. We also reviewed the reload logic to handle missing config gracefully — if the streaming config is unavailable, the service continues serving from batch indexes only. Streaming is degraded but the service stays up.

### What I Learned

This incident shaped how I think about three things:

**Data lifecycle separation.** Ephemeral data (indexes that get rebuilt) and persistent data (config that's written once) should never share a storage boundary where a blanket policy can destroy both. Different TTL expectations mean different buckets.

**Graceful degradation over fatal failure.** A missing config file should trigger an alert and a fallback, not kill the service. The streaming feature is an enhancement — losing it shouldn't take down the entire retrieval pipeline.

**Infrastructure as a feature dependency.** When you add a new file to an existing bucket, you're not just storing data — you're inheriting every policy, lifecycle rule, and access pattern that bucket already has. Feature launch checklists should include an infra review: what policies exist on this storage, and does my new data's lifecycle match?

---

## Post-Mortem

### Incident Summary

| Field | Detail |
|---|---|
| **Date** | ~30 days after streaming ANN production rollout |
| **Duration** | Approximately 30-60 minutes (detection to recovery) |
| **Severity** | P1 — Content retrieval degraded, ann-service pods crash-looping |
| **Impact** | Spotlight content discovery degraded. Users served stale or reduced results. |
| **Root cause** | GCS lifecycle policy silently deleted streaming config CSV from shared index bucket |
| **Detection** | Pod crash-loop alerts / on-call page |

### Timeline

```
T-30 days:    Streaming ANN feature launched to production.
              streaming_config.csv written to gs://snap-ann-indexes/.
              Feature works perfectly. A/B metrics positive.

T-0 (incident start):
  HH:MM       GCS lifecycle policy runs.
              streaming_config.csv age > N days.
              File silently deleted.

  HH:MM+2m    ann-service periodic config reload fires.
              Reads streaming_config.csv from GCS → 404.
              Pod treats missing config as fatal → crash.
              K8s restarts pod → boot reads config → 404 → crash.
              Crash loop across all pods in the deployment.

  HH:MM+5m    Alerting fires: ann-service pod restart rate > threshold.
              On-call paged.

  HH:MM+10m   On-call checks recent deploys → none.
              Checks permissions → unchanged.
              Checks GCS bucket → file is missing.
              "Who deleted streaming_config.csv?"

  HH:MM+20m   GCS audit logs checked.
              Lifecycle policy deletion event found.
              Root cause identified.

  HH:MM+25m   Config file restored to bucket manually.
              Pods pick up config on next restart.
              Service recovers.

  HH:MM+30m   All pods healthy. Retrieval metrics back to normal.
              Incident mitigated.
```

### Root Cause Analysis

```
                    ┌─────────────────────────────┐
                    │   Proximate cause            │
                    │   GCS lifecycle policy       │
                    │   deleted the config file    │
                    └──────────────┬──────────────┘
                                   │ why?
                    ┌──────────────▼──────────────┐
                    │   Config file stored in      │
                    │   a bucket with a TTL rule   │
                    │   meant for index files      │
                    └──────────────┬──────────────┘
                                   │ why?
                    ┌──────────────▼──────────────┐
                    │   No review of bucket        │
                    │   policies when adding       │
                    │   new file types              │
                    └──────────────┬──────────────┘
                                   │ why?
                    ┌──────────────▼──────────────┐
                    │   Feature launch process     │
                    │   didn't include infra       │
                    │   policy review step         │
                    └──────────────┬──────────────┘
                                   │ why?
                    ┌──────────────▼──────────────┐
                    │   Root cause:                │
                    │   No org-level practice of   │
                    │   auditing storage policies  │
                    │   when bucket usage evolves  │
                    └─────────────────────────────┘
```

**Contributing factors:**

1. **Shared storage boundary.** Ephemeral data (indexes, rebuilt regularly) and persistent data (config, written once) shared the same bucket and lifecycle rule.

2. **No monitoring on lifecycle deletions.** GCS lifecycle deletions are silent. No alert fires when an object is removed by policy. The team had no visibility into what the policy was deleting.

3. **Fatal failure mode on missing config.** The ann-service treated a missing streaming config as unrecoverable rather than falling back to batch-only mode. This turned a data availability issue into a full service outage.

4. **No feature-launch infra review.** The launch process validated the feature (A/B test, metrics, rollout) but did not review the infrastructure the feature depended on (bucket policies, retention rules).

### Impact Assessment

| Area | Impact |
|---|---|
| **User-facing** | Spotlight content discovery returned stale or reduced results during the outage window |
| **Service** | ann-service fully unavailable (crash-loop). Upstream services using retrieval received errors or fell back to cached results |
| **Data** | No data loss — index files were unaffected. Only the config CSV was deleted and was easily restored |
| **Duration** | ~30 minutes from detection to full recovery |

### Remediation Actions

#### Immediate (within 24 hours)

| # | Action | Owner | Status |
|---|---|---|---|
| 1 | Restore streaming_config.csv to GCS bucket | On-call | Done |
| 2 | Verify all ann-service pods healthy and serving | On-call | Done |
| 3 | Verify streaming ANN consuming from Kafka correctly | On-call | Done |

#### Short-term (within 1 week)

| # | Action | Owner |
|---|---|---|
| 4 | Move config files to a separate GCS bucket with no lifecycle policy (`gs://snap-ann-config/`) |  |
| 5 | Update ann-service to handle missing streaming config gracefully — fall back to batch-only mode with an alert instead of crashing |  |
| 6 | Add GCS object monitoring: alert if `streaming_config.csv` is deleted or missing |  |
| 7 | Audit all lifecycle policies on buckets owned by the Retrieval team |  |

#### Long-term (within 1 quarter)

| # | Action | Owner |
|---|---|---|
| 8 | Add infra policy review to feature launch checklist: "Does the storage I'm using have lifecycle/retention policies? Does my data's lifecycle match?" |  |
| 9 | Org-wide audit of GCS lifecycle policies — identify buckets where persistent and ephemeral data are mixed |  |
| 10 | Implement lifecycle policy dry-run tooling: before a policy runs, report what it WOULD delete. Alert if unexpected object types are in the blast radius |  |
| 11 | Move all GCS lifecycle policies to Terraform/IaC so changes go through code review |  |
| 12 | Add a "storage policy" section to the service onboarding template — every new bucket documents its lifecycle rules and what data types are allowed |  |

### Prevention Framework

#### For the specific failure mode (lifecycle deletes critical files)

```
1. SEPARATE BY LIFECYCLE
   Data with different TTL expectations must live in
   different buckets. A blanket policy on a shared bucket
   is a time bomb.

   Rule: If you're adding a file to an existing bucket and
   your file should live longer than the bucket's lifecycle
   policy allows → use a different bucket.

2. MONITOR DESTRUCTIVE OPERATIONS
   Every lifecycle policy should have a corresponding alert:
     ─ "Objects deleted by lifecycle in bucket X > threshold"
     ─ "Specific critical objects missing" (existence check)

3. DEGRADE, DON'T CRASH
   Any config read from external storage should have a
   fallback path. The service's core function (serving ANN
   results) should survive the loss of an enhancement
   (streaming mode).
```

#### For the class of failure (delayed infrastructure time bombs)

```
1. FEATURE LAUNCHES MUST REVIEW INFRA DEPENDENCIES
   Not just "does the feature work?" but "what existing
   infrastructure am I depending on, and what policies/
   behaviors does it have that could affect my feature?"

   Checklist:
     □ Storage: lifecycle policies, retention, replication
     □ Networking: firewall rules, rate limits, DNS TTLs
     □ Compute: autoscaling limits, preemption policies
     □ Cron/background: scheduled jobs that touch this data

2. INFRASTRUCTURE CONFIG IS CODE
   Lifecycle policies, IAM rules, bucket configs — all in
   Terraform/Pulumi, all go through code review. No console
   clicks for production infrastructure.

3. PERIODIC INFRA AUDITS
   Storage policies set years ago may no longer match current
   usage. Quarterly review of lifecycle rules vs actual
   bucket contents catches drift before it causes incidents.

4. TIME-BOMB TESTING
   For features that store persistent data, test what happens
   when that data disappears. Chaos engineering for storage:
     ─ Delete the config file in staging
     ─ Verify the service degrades gracefully
     ─ Verify alerts fire
```

### Key Takeaway

> The most dangerous production failures are the ones with a delay between cause and effect. A lifecycle policy set years ago, a config file added last month, and a deletion that happens silently 30 days later — none of these events individually look like a problem. The incident only exists because of their intersection in time. Designing resilient systems means designing for **invisible interactions** between components that were never intended to affect each other.
