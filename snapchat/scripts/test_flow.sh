#!/bin/bash
# End-to-end smoke test for Snapchat MVP
# Requires all services running (see Makefile)

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $1${NC}"; }
fail() { echo -e "${RED}✗ $1${NC}"; exit 1; }

BASE="http://localhost:8080"

echo "=== Snapchat MVP E2E Test ==="
echo ""

# 1. Health checks
echo "--- Health Checks ---"
curl -sf "$BASE/health" > /dev/null && pass "Gateway" || fail "Gateway"
curl -sf "http://localhost:8081/health" > /dev/null && pass "Registration" || fail "Registration"
curl -sf "http://localhost:8082/health" > /dev/null && pass "Auth" || fail "Auth"
curl -sf "http://localhost:8090/health" > /dev/null && pass "Ingestion" || fail "Ingestion"
curl -sf "http://localhost:8091/health" > /dev/null && pass "Retrieval" || fail "Retrieval"
curl -sf "http://localhost:8092/health" > /dev/null && pass "Ranking" || fail "Ranking"
echo ""

# 2. Registration
echo "--- Registration Flow ---"
REG=$(curl -sf -X POST "$BASE/api/v1/register" \
  -H "Content-Type: application/json" \
  -d '{"phone": "+15551234567", "device_id": "e2e-test"}')
REQ_ID=$(echo "$REG" | python3 -c "import sys,json; print(json.load(sys.stdin)['request_id'])")
pass "Register: request_id=$REQ_ID"

CODE=$(docker exec snap-postgres psql -U snapuser -d snapchat -t -c \
  "SELECT code FROM verification_codes WHERE request_id = '$REQ_ID';" | tr -d ' \n')
pass "Code from DB: $CODE"

VERIFY=$(curl -sf -X POST "$BASE/api/v1/verify" \
  -H "Content-Type: application/json" \
  -d "{\"request_id\": \"$REQ_ID\", \"code\": \"$CODE\"}")
USER_ID=$(echo "$VERIFY" | python3 -c "import sys,json; print(json.load(sys.stdin)['user_id'])")
pass "Verify: user_id=$USER_ID"
echo ""

# 3. Auth
echo "--- Auth Flow ---"
TOKENS=$(curl -sf -X POST "http://localhost:8082/issue" \
  -H "Content-Type: application/json" \
  -d "{\"user_id\": \"$USER_ID\"}")
ACCESS=$(echo "$TOKENS" | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])")
pass "Token issued"

# Protected endpoint without token
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/v1/feed")
[ "$STATUS" = "401" ] && pass "Unauthenticated → 401" || fail "Expected 401, got $STATUS"
echo ""

# 4. Content Upload
echo "--- Content Pipeline ---"
CID=$(curl -sf -X POST "$BASE/api/v1/content" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS" \
  -d '{"title": "E2E Test Video", "category": "comedy"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['content_id'])")
pass "Content created: $CID"

sleep 2  # wait for Kafka consumer
INDEX_SIZE=$(curl -sf "http://localhost:8091/debug/index" | python3 -c "import sys,json; print(json.load(sys.stdin)['total_items'])")
pass "HNSW index size: $INDEX_SIZE"
echo ""

# 5. Feed
echo "--- Feed ---"
FEED=$(curl -sf "$BASE/api/v1/feed?limit=3" -H "Authorization: Bearer $ACCESS")
ITEMS=$(echo "$FEED" | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']))")
[ "$ITEMS" -gt "0" ] && pass "Feed returned $ITEMS items" || fail "Feed returned 0 items"
echo ""

# 6. Debug endpoints
echo "--- Debug ---"
curl -sf "http://localhost:8091/debug/ring" > /dev/null && pass "Hash ring endpoint" || fail "Hash ring"
curl -sf "http://localhost:8091/debug/leader" > /dev/null && pass "Leader election endpoint" || fail "Leader election"
curl -sf "http://localhost:8092/debug/circuit" > /dev/null && pass "Circuit breaker endpoint" || fail "Circuit breaker"
echo ""

echo "=== All tests passed ==="
