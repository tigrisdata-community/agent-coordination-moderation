package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Store wraps S3 operations against a Tigris bucket.
type Store struct {
	client     *s3.Client
	bucketName string
}

// New creates a Store configured for Tigris via the AWS SDK v2.
// It reads credentials from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
// and the endpoint from AWS_ENDPOINT_URL (default: https://fly.storage.tigris.dev).
func New(ctx context.Context, bucketName string) (*Store, error) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		endpoint = "https://fly.storage.tigris.dev"
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("auto"))
	if err != nil {
		return nil, fmt.Errorf("store: load aws config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.Region = "auto"
		o.UsePathStyle = false
	})

	return &Store{client: client, bucketName: bucketName}, nil
}

// Write marshals v as JSON and writes it to key in the bucket.
func (s *Store) Write(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucketName),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("store: put %s: %w", key, err)
	}
	return nil
}

// Read fetches the object at key, unmarshals JSON into v, and returns the ETag.
func (s *Store) Read(ctx context.Context, key string, v any) (string, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("store: get %s: %w", key, err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("store: read body %s: %w", key, err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return "", fmt.Errorf("store: unmarshal %s: %w", key, err)
	}

	etag := ""
	if out.ETag != nil {
		etag = strings.Trim(*out.ETag, `"`)
	}
	return etag, nil
}

// Move atomically renames src to dst within the bucket using the Tigris
// X-Tigris-Rename header. This updates metadata in place without rewriting
// the object data.
// See: https://www.tigrisdata.com/docs/objects/object-rename/
func (s *Store) Move(ctx context.Context, src, dst string) error {
	copySource := s.bucketName + "/" + src

	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucketName),
		CopySource: aws.String(copySource),
		Key:        aws.String(dst),
	}, withHeader("X-Tigris-Rename", "true"))
	if err != nil {
		return fmt.Errorf("store: rename %s -> %s: %w", src, dst, err)
	}
	return nil
}

// Delete removes the object at key.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("store: delete %s: %w", key, err)
	}
	return nil
}

// withHeader adds a custom HTTP header to an S3 API call.
func withHeader(key, value string) func(*s3.Options) {
	return func(options *s3.Options) {
		options.APIOptions = append(options.APIOptions,
			smithyhttp.AddHeaderValue(key, value),
		)
	}
}
