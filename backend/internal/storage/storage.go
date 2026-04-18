// Package storage provides a thin wrapper around MinIO object storage.
// It is intentionally agnostic to users and business logic — callers are
// responsible for building object keys and interpreting errors.
package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Backend is the interface implemented by Client and used by handlers.
// Defining it here (rather than in the api package) keeps the storage
// package self-contained and makes it easy to swap implementations in tests.
type Backend interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, int64, error)
	DeleteObject(ctx context.Context, key string) error
	DeleteUserObjects(ctx context.Context, userID int64) error
}

// Client wraps a MinIO client and the target bucket name.
type Client struct {
	mc     *minio.Client
	bucket string
}

// New creates and returns a storage Client.
// endpoint is host:port without a scheme (e.g. "localhost:9000").
func New(endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Client, error) {
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("creating minio client: %w", err)
	}
	return &Client{mc: mc, bucket: bucket}, nil
}

// ObjectKey returns the canonical object key for a backed-up file.
// Format: "{userID}/{watchedPathID}/{relativePath}" — e.g. "1/3/photos/2024/img.jpg".
func ObjectKey(userID, watchedPathID int64, relativePath string) string {
	return fmt.Sprintf("%d/%d/%s", userID, watchedPathID, relativePath)
}

// PutObject streams r into object storage under key.
// size must be the exact byte count (used as Content-Length); pass -1 only
// if the size is genuinely unknown (disables MinIO's multipart optimisation).
func (c *Client) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("putting object %q: %w", key, err)
	}
	return nil
}

// GetObject returns a streaming reader for the given object key and its size.
// The caller must close the returned ReadCloser.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("getting object %q: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, 0, fmt.Errorf("stat object %q: %w", key, err)
	}
	return obj, info.Size, nil
}

// DeleteObject removes a single object.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	err := c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("deleting object %q: %w", key, err)
	}
	return nil
}

// DeleteUserObjects removes all objects whose key starts with "{userID}/".
// Called when a user changes their watched path — all prior backups are stale.
func (c *Client) DeleteUserObjects(ctx context.Context, userID int64) error {
	prefix := fmt.Sprintf("%d/", userID)
	objectsCh := c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})
	for object := range objectsCh {
		if object.Err != nil {
			return fmt.Errorf("listing objects with prefix %q: %w", prefix, object.Err)
		}
		if err := c.DeleteObject(ctx, object.Key); err != nil {
			return err
		}
	}
	return nil
}
