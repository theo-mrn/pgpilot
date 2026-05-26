package storage

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// CheckBucket verifies that the bucket exists and the credentials have access.
// endpoint is empty for AWS S3, or the MinIO/compatible URL otherwise.
// region is used for AWS S3; ignored for endpoint-based access.
func CheckBucket(bucket, accessKey, secretKey, region, endpoint string) error {
	creds := aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""))

	if region == "" {
		region = "us-east-1"
	}

	opts := s3.Options{
		Credentials: creds,
		Region:      region,
	}
	if endpoint != "" {
		opts.BaseEndpoint = aws.String(endpoint)
		opts.UsePathStyle = true
	}

	client := s3.New(opts)

	_, err := client.HeadBucket(context.Background(), &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return fmt.Errorf("cannot access bucket %q: %w", bucket, err)
	}
	return nil
}
