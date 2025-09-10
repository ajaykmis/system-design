# Delayed Payment Scheduler Service Design

## 1. System Components & Database Architecture

### Core Services
```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   API Service   │    │   Hold Service  │    │ Payment Service │
│                 │    │                 │    │                 │
│ - User requests │    │ - Hold logic    │    │ - Fund transfer │
│ - Validation    │───▶│ - State mgmt    │───▶│ - Balance check │
│ - Rate limiting │    │ - Scheduling    │    │ - Retry logic   │
│ - Auth/AuthZ    │    │ - Cancellation  │    │ - Audit logs    │
└─────────────────┘    └─────────────────┘    └─────────────────┘
```

### Database Layout (3 Primary Databases)
```
┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐
│   User DB        │    │   Payment DB     │    │   Audit DB       │
│                  │    │                  │    │                  │
│ - user_accounts  │    │ - payment_holds  │    │ - audit_events   │
│ - user_balances  │    │ - hold_events    │    │ - system_logs    │
│ - reservations   │    │ - retry_queue    │    │ - metrics        │
│                  │    │                  │    │                  │
│ PostgreSQL       │    │ PostgreSQL       │    │ PostgreSQL       │
│ (Sharded by      │    │ (Partitioned by  │    │ (Time-series     │
│  user_id)        │    │  execute_date)   │    │  optimized)      │
└──────────────────┘    └──────────────────┘    └──────────────────┘
```

### Message Queue Architecture
```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Delay Queue   │    │Processing Queue │    │   DLQ Queue     │
│                 │    │                 │    │                 │
│ - AWS SQS       │───▶│ - Ready holds   │───▶│ - Failed holds  │
│ - Delay up to   │    │ - Immediate     │    │ - Manual review │
│   15 minutes    │    │   processing    │    │ - Alerting      │
└─────────────────┘    └─────────────────┘    └─────────────────┘
          │                       │                       │
          ▼                       ▼                       ▼
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Long Delay    │    │   Processors    │    │   Ops Tools     │
│     Store       │    │                 │    │                 │
│                 │    │ - Auto-scaling  │    │ - Replay failed │
│ - Redis TTL     │    │ - Load balance  │    │ - Manual retry  │
│ - For delays    │    │ - Health check  │    │ - Root cause    │
│   > 15 min      │    │                 │    │   analysis      │
└─────────────────┘    └─────────────────┘    └─────────────────┘
```

## 2. Detailed Database Schemas

### User Database (Sharded by user_id)
```
user_accounts:
- user_id (BIGINT, primary key)
- username (VARCHAR)
- status (ENUM: ACTIVE/SUSPENDED/DELETED)
- created_at (TIMESTAMP)

user_balances:
- user_id (BIGINT, primary key)
- available_balance (BIGINT) -- Available Robux
- reserved_balance (BIGINT)  -- Reserved for holds
- total_balance (BIGINT)     -- available + reserved
- last_updated (TIMESTAMP)
- version (INTEGER)          -- Optimistic locking

balance_reservations:
- reservation_id (UUID, primary key)
- user_id (BIGINT)
- amount (BIGINT)
- hold_id (UUID)
- created_at (TIMESTAMP)
- expires_at (TIMESTAMP)
- status (ENUM: ACTIVE/RELEASED/EXPIRED)
```

### Payment Database (Partitioned by execute_date)
```
payment_holds:
- hold_id (UUID, primary key)
- from_user_id (BIGINT)
- to_user_id (BIGINT)
- amount (BIGINT)
- created_at (TIMESTAMP)
- execute_at (TIMESTAMP)
- status (VARCHAR: PENDING/PROCESSING/COMPLETED/CANCELLED/FAILED)
- reservation_id (UUID)
- retry_count (INTEGER, default 0)
- last_error (TEXT)
- metadata (JSONB)

hold_events:
- event_id (UUID, primary key)
- hold_id (UUID)
- event_type (VARCHAR: CREATED/SCHEDULED/PROCESSING/COMPLETED/FAILED)
- from_status (VARCHAR)
- to_status (VARCHAR)
- created_at (TIMESTAMP)
- details (JSONB)
```

### Audit Database (Time-series optimized)
```
audit_events:
- event_id (UUID, primary key)
- user_id (BIGINT)
- action_type (VARCHAR)
- resource_type (VARCHAR)
- resource_id (UUID)
- timestamp (TIMESTAMP)
- details (JSONB)
- ip_address (INET)
- user_agent (TEXT)

system_metrics:
- metric_id (UUID, primary key)
- metric_name (VARCHAR)
- metric_value (DOUBLE)
- labels (JSONB)
- timestamp (TIMESTAMP)
```

## 3. Event-Driven Architecture Flow

### Message Flow Diagram
```
┌─────────────┐   create_hold   ┌─────────────┐   reserve_funds   ┌─────────────┐
│             │ ─────────────▶  │             │ ─────────────▶    │             │
│ API Service │                 │Hold Service │                   │User Service │
│             │ ◀─────────────  │             │ ◀─────────────    │             │
└─────────────┘   hold_created  └─────────────┘   funds_reserved  └─────────────┘
       │                               │                                 │
       │                               ▼                                 │
       │                    ┌─────────────┐                              │
       │                    │             │                              │
       │                    │Delay Queue  │                              │
       │                    │(SQS/Redis)  │                              │
       │                    │             │                              │
       │                    └─────────────┘                              │
       │                               │                                 │
       │                               │ (after delay)                   │
       │                               ▼                                 │
       │                    ┌─────────────┐   execute_transfer  ┌─────────────┐
       │                    │             │ ─────────────▶      │             │
       │                    │Payment      │                     │Payment      │
       │                    │Processor    │ ◀─────────────      │Service      │
       │                    │             │   transfer_result   │             │
       │                    └─────────────┘                     └─────────────┘
       │                               │                                 │
       │                               ▼                                 │
       ▼                    ┌─────────────┐                              ▼
┌─────────────┐             │             │                    ┌─────────────┐
│             │◀────────────│Audit Service│───────────────────▶│             │
│Notification │             │             │                    │Metrics      │
│Service      │             └─────────────┘                    │Service      │
└─────────────┘                                                └─────────────┘
```

## 4. End-to-End Flow: Positive Case

### Step-by-Step Flow: UserA pays 100 Robux to UserB after 48 hours

```
Timeline: Create Hold Request
┌─────────────┐
│  t=0        │  API Request: POST /holds
│  UserA      │  Body: {to_user: UserB, amount: 100, delay: 48h}
│             │
└─────────────┘
       │
       ▼
┌─────────────┐  1. Validate Request
│             │  ✓ UserA exists and active
│ API Service │  ✓ UserB exists and active  
│             │  ✓ Amount > 0 and <= daily_limit
│             │  ✓ delay <= max_allowed (7 days)
└─────────────┘
       │
       ▼
┌─────────────┐  2. Check & Reserve Funds
│             │  Query: SELECT available_balance FROM user_balances 
│User Service │         WHERE user_id = UserA
│             │  ✓ available_balance (250) >= amount (100)
│             │  Reserve: available_balance -= 100, reserved_balance += 100
└─────────────┘
       │
       ▼
┌─────────────┐  3. Create Hold Record
│             │  INSERT INTO payment_holds (
│Hold Service │    hold_id: uuid_generate_v4(),
│             │    from_user_id: UserA,
│             │    to_user_id: UserB,
│             │    amount: 100,
│             │    execute_at: now() + 48h,
│             │    status: 'PENDING'
│             │  )
└─────────────┘
       │
       ▼
┌─────────────┐  4. Schedule Delayed Message
│             │  For delay > 15min: Store in Redis with TTL
│Delay Queue  │  SET hold:uuid {hold_id, execute_at} EX 172800
│             │  
│             │  Background job checks Redis every minute for expired keys
│             │  When TTL expires → move to immediate processing queue
└─────────────┘
```

```
Timeline: Execute Payment (t = 48 hours later)
┌─────────────┐
│ t=48h       │  Redis TTL expires → Background job triggered
│             │
└─────────────┘
       │
       ▼
┌─────────────┐  5. Retrieve Hold for Processing
│             │  GET hold:uuid from Redis → extract hold_id
│Processing   │  Query: SELECT * FROM payment_holds WHERE hold_id = ?
│Queue        │  Publish message to immediate processing queue
│             │
└─────────────┘
       │
       ▼
┌─────────────┐  6. Process Payment
│             │  BEGIN TRANSACTION
│Payment      │    UPDATE payment_holds SET status = 'PROCESSING'
│Processor    │    SELECT reservation_id FROM payment_holds WHERE hold_id = ?
│             │    
└─────────────┘
       │
       ▼
┌─────────────┐  7. Execute Fund Transfer
│             │  -- Debit from UserA reserved balance
│Payment      │  UPDATE user_balances 
│Service      │  SET reserved_balance = reserved_balance - 100
│             │  WHERE user_id = UserA
│             │
│             │  -- Credit to UserB available balance  
│             │  UPDATE user_balances
│             │  SET available_balance = available_balance + 100
│             │  WHERE user_id = UserB
└─────────────┘
       │
       ▼
┌─────────────┐  8. Update Hold Status
│             │  UPDATE payment_holds 
│Hold Service │  SET status = 'COMPLETED', 
│             │      completed_at = now()
│             │  WHERE hold_id = ?
│             │
│             │  DELETE FROM balance_reservations 
│             │  WHERE reservation_id = ?
│             │  COMMIT TRANSACTION
└─────────────┘
       │
       ▼
┌─────────────┐  9. Audit & Notifications
│             │  INSERT INTO audit_events (
│Audit        │    action: 'PAYMENT_COMPLETED',
│Service      │    user_id: UserA,
│             │    details: {amount: 100, recipient: UserB}
│             │  )
│             │
│             │  Send notifications to UserA & UserB
│             │  Update metrics (payments_completed_total++)
└─────────────┘
```

## 5. Scalability & Reliability Features

### Auto-Scaling Components
```
┌─────────────────────────────────────────────────────────────────┐
│                    Load Balancer (HAProxy)                      │
└─────────────────────┬───────────────────────────────────────────┘
                      │
        ┌─────────────┼─────────────┐
        │             │             │
        ▼             ▼             ▼
┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│ API Service │ │ API Service │ │ API Service │
│ Instance 1  │ │ Instance 2  │ │ Instance N  │
└─────────────┘ └─────────────┘ └─────────────┘
        │             │             │
        └─────────────┼─────────────┘
                      │
        ┌─────────────┼─────────────┐
        │             │             │
        ▼             ▼             ▼
┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│Payment Proc │ │Payment Proc │ │Payment Proc │
│ Instance 1  │ │ Instance 2  │ │ Instance N  │
└─────────────┘ └─────────────┘ └─────────────┘
```

### Database Sharding Strategy
```
Users 1-33M     Users 34-67M    Users 68-100M
┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│  Shard A    │ │  Shard B    │ │  Shard C    │
│             │ │             │ │             │
│ Primary DB  │ │ Primary DB  │ │ Primary DB  │
│     │       │ │     │       │ │     │       │
│     ▼       │ │     ▼       │ │     ▼       │
│ Read        │ │ Read        │ │ Read        │
│ Replica 1   │ │ Replica 1   │ │ Replica 1   │
│     │       │ │     │       │ │     │       │
│     ▼       │ │     ▼       │ │     ▼       │
│ Read        │ │ Read        │ │ Read        │
│ Replica 2   │ │ Replica 2   │ │ Replica 2   │
└─────────────┘ └─────────────┘ └─────────────┘
```

## 6. Key Design Decisions

### Database Choice: PostgreSQL
**Reasoning**: ACID compliance critical for financial transactions, excellent partitioning support, JSONB for flexible metadata, proven at scale.

### Message Queue: Hybrid Approach
**Short delays (< 15 min)**: AWS SQS native delay feature
**Long delays (> 15 min)**: Redis TTL + background scheduler
**Reasoning**: SQS has 15-minute delay limit; Redis provides cost-effective long-term storage with precise timing.

### Fund Reservation: Immediate
**Reasoning**: Prevents insufficient funds at execution time, provides better user experience, essential for financial accuracy.

### Consistency Model: Strong for payments, Eventual for queries  
**Reasoning**: Financial operations require ACID guarantees, user status queries can tolerate slight delays for better performance.

This architecture handles millions of concurrent delayed payments while maintaining financial accuracy, system reliability, and operational excellence at Roblox scale.
