CRITICAL FLOWS
Usecase 1: User A sends message to User B
Input: chatId: chat_1, messageId: msg_123, message: "Hello!"

1. Messages Table Write:
   PK: chat_1
   SK: 1704067200000#msg_123
   {
     "chatId": "chat_1",
     "messageId": "msg_123", 
     "senderId": "user_A",
     "encryptedContent": "aGVsbG8h",
     "timestamp": 1704067200000,
     "ttlSeconds": 86400,
     "expiresAt": 1704153600,
     "deliveryStatus": "SENT"
   }

2. Chats Table Update:
   PK: chat_1
   {
     "lastMessageAt": 1704067200000,
     "lastMessageId": "msg_123",
     "lastMessagePreview": "Hello!",
     "messageCount": 46,
     "updatedAt": 1704067200000
   }

3. UserChatMapping Updates (2 writes):
   PK: user_A, SK: 1704067200000#chat_1
   PK: user_B, SK: 1704067200000#chat_1
   - Updates both users' chat lists with new activity
Why this works: Single chat lookup + atomic message write + efficient user chat list updates

Usecase 2: User B receives message (via push notification)
1. Delivery Service Query:
   - Get chat participants from Chats table: PK=chat_1
   - Find user_B in participants array
   - Check WebSocket connection in Redis cache

2. Real-time Delivery:
   - Push via WebSocket if online
   - Send push notification if offline
   - Update delivery status via Kafka

3. Status Update:
   Messages Table Update:
   PK: chat_1, SK: 1704067200000#msg_123
   SET deliveryStatus = "DELIVERED"

4. UserChatMapping Update:
   PK: user_B, SK: 1704067200000#chat_1
   INCREMENT unreadCount
Why this works: Direct participant lookup + efficient connection state checking + async status updates

Usecase 3: User A loads chat history and scrolls
Initial Load (20 messages):
Query Messages Table:
PK = chat_1
SK begins_with current_timestamp
Limit = 20
ScanIndexForward = False (newest first)

Pagination (next 20):
Query Messages Table:
PK = chat_1  
SK < last_timestamp_from_previous_query
Limit = 20

Response per message:
{
  "messageId": "msg_123",
  "senderId": "user_A", 
  "content": "decrypted_content",
  "timestamp": 1704067200000,
  "deliveryStatus": "READ"
}
Why this works: Single partition query with timestamp-based pagination. O(log n) performance with automatic DynamoDB sorting.

GROUP MESSAGING FLOWS
Usecase 4: User A sends message to group chat (5 people)
Input: chatId: group_789, participants: [user_A, user_B, user_C, user_D, user_E]

1. Messages Table (same as 1-1):
   PK: group_789, SK: timestamp#messageId

2. Chats Table Update:
   PK: group_789
   participants: ["user_A", "user_B", "user_C", "user_D", "user_E"]

3. UserChatMapping Updates (5 writes):
   PK: user_A, SK: timestamp#group_789
   PK: user_B, SK: timestamp#group_789  
   PK: user_C, SK: timestamp#group_789
   PK: user_D, SK: timestamp#group_789
   PK: user_E, SK: timestamp#group_789

4. Kafka Message:
   Topic: messages
   Partition: hash(group_789)
   Recipients: [user_B, user_C, user_D, user_E] (exclude sender)
Why this works: Same schema handles groups. Kafka fan-out to multiple recipients. UserChatMapping keeps each user's view updated.

Usecase 5: User loads their chat list (inbox)
Query UserChatMapping:
PK = user_A
Limit = 50
ScanIndexForward = False (most recent first)

Response:
[
  {
    "chatId": "group_789",
    "lastMessageAt": 1704067800000,
    "lastMessagePreview": "Hey everyone!",
    "unreadCount": 3,
    "otherParticipants": ["user_B", "user_C", "user_D", "user_E"],
    "chatType": "GROUP"
  },
  {
    "chatId": "chat_1", 
    "lastMessageAt": 1704067200000,
    "lastMessagePreview": "Hello!",
    "unreadCount": 0,
    "otherParticipants": ["user_B"],
    "chatType": "PRIVATE"
  }
]
Why this works: Single query gets all user's chats, pre-sorted by activity. No joins needed - everything denormalized.

GOOD-TO-HAVE FLOWS
Usecase 6: Mark messages as read
1. Batch Update Messages:
   For each unread messageId in chat:
   PK: chatId, SK: timestamp#messageId
   SET deliveryStatus = "READ", readAt = current_timestamp

2. Update Chats table participant status:
   PK: chatId
   SET participantStatuses.user_A.lastSeenAt = current_timestamp

3. Reset UserChatMapping unread count:
   PK: user_A, SK: timestamp#chatId
   SET unreadCount = 0
Usecase 7: Search for specific message
Query Messages GSI:
messageId-index
PK = msg_123

Returns: Full message details + chatId for context
Usecase 8: Delete expired messages (ephemeral)
DynamoDB TTL automatically deletes:
- Messages table items where expiresAt < current_time
- No manual cleanup needed
- Triggers DynamoDB Streams for cleanup notifications

DESIGN STRENGTHS

Single-digit millisecond queries - All critical paths use primary keys
Automatic scaling - DynamoDB handles traffic spikes
Efficient pagination - Timestamp-based cursors
Consistent chat ordering - UserChatMapping pre-sorts by activity
Ephemeral cleanup - Native DynamoDB TTL
Flexible participant counts - Same schema for 1-1 and groups
Real-time updates - Kafka ensures all participants get updates

TRADE-OFFS

Write amplification for groups (N UserChatMapping updates)
Storage overhead from denormalization
Eventual consistency between tables
GSI costs for secondary access patterns

The design prioritizes read performance and user experience over write efficiency, which aligns with messaging app usage patterns (more reads than writes).
