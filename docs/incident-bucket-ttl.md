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

## Interview Framing

This incident demonstrates several Staff-level concepts:

| Concept | How it applies |
|---|---|
| **Separation of concerns** | Different data lifecycles should have different storage boundaries |
| **Blast radius** | One lifecycle rule affected an unrelated feature |
| **Graceful degradation** | Config read failure should fall back, not crash |
| **Operational awareness** | Silent background processes need observability |
| **Feature launch rigor** | New features in existing infra need infra review |
| **Delayed failures** | Time-bomb bugs that work fine on launch but fail later |
| **Incident debugging** | Tracing a missing file to a lifecycle policy via audit logs |
