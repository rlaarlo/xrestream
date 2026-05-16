package relay

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type R2Client struct {
	client    *s3.Client
	bucket    string
	publicURL string
}

func NewR2Client(accountID, accessKeyID, secretAccessKey, bucket, publicURL string) *R2Client {
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "auto",
		Credentials: aws.NewCredentialsCache(
			credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		),
		UsePathStyle: true,
	})
	return &R2Client{
		client:    client,
		bucket:    bucket,
		publicURL: strings.TrimRight(publicURL, "/"),
	}
}

func (r *R2Client) Upload(ctx context.Context, slug, ref string, body io.Reader, contentType string) error {
	key := slug + "/" + ref
	input := &s3.PutObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	_, err := r.client.PutObject(ctx, input)
	return err
}

func (r *R2Client) PublicURL(slug, ref string) string {
	return r.publicURL + "/" + slug + "/" + ref
}

func (r *R2Client) DeleteFolder(ctx context.Context, slug string) error {
	prefix := slug + "/"
	paginator := s3.NewListObjectsV2Paginator(r.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(r.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			if _, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(r.bucket),
				Key:    obj.Key,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteObject removes a single object by slug/ref.
func (r *R2Client) DeleteObject(ctx context.Context, slug, ref string) error {
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(slug + "/" + ref),
	})
	return err
}

// ListOlderThan returns object keys (slug/name) under the slug prefix whose
// LastModified is older than now-age. Used by retention cleanup.
func (r *R2Client) ListOlderThan(ctx context.Context, slug string, age time.Duration) ([]string, error) {
	cutoff := time.Now().Add(-age)
	prefix := slug + "/"
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(r.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(r.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			if obj.Key == nil || obj.LastModified == nil {
				continue
			}
			if obj.LastModified.Before(cutoff) {
				keys = append(keys, *obj.Key)
			}
		}
	}
	return keys, nil
}

// DeleteKeys removes a batch of full keys (already in slug/name form).
func (r *R2Client) DeleteKeys(ctx context.Context, keys []string) error {
	for _, key := range keys {
		k := key
		if _, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(r.bucket),
			Key:    aws.String(k),
		}); err != nil {
			return err
		}
	}
	return nil
}
