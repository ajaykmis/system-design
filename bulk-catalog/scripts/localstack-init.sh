#!/bin/bash
# Runs inside LocalStack on startup — creates S3 buckets and SQS queue.
set -e

echo "Creating S3 buckets..."
awslocal s3 mb s3://bulk-catalog-uploads
awslocal s3 mb s3://bulk-catalog-errors

echo "Creating SQS queue..."
awslocal sqs create-queue --queue-name bulk-catalog-jobs

# Wire S3 ObjectCreated events → SQS
QUEUE_ARN=$(awslocal sqs get-queue-attributes \
  --queue-url http://localhost:4566/000000000000/bulk-catalog-jobs \
  --attribute-names QueueArn \
  --query 'Attributes.QueueArn' --output text)

awslocal s3api put-bucket-notification-configuration \
  --bucket bulk-catalog-uploads \
  --notification-configuration "{
    \"QueueConfigurations\": [{
      \"QueueArn\": \"$QUEUE_ARN\",
      \"Events\": [\"s3:ObjectCreated:*\"]
    }]
  }"

echo "LocalStack init complete."
echo "  Upload bucket : bulk-catalog-uploads"
echo "  Error bucket  : bulk-catalog-errors"
echo "  SQS queue     : bulk-catalog-jobs"
