// Package storage provides a thin wrapper around MinIO object storage.
// It is intentionally agnostic to users and business logic — callers are
// responsible for building object keys and interpreting errors.
package storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Backend is the interface implemented by Client and used by handlers.
type Backend interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, int64, error)
	ObjectExists(ctx context.Context, key string) (bool, error)
	CopyObject(ctx context.Context, srcKey, dstKey string) error
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
// Format: "{userID}/{watchedPathID}/{relativePath}".
//
// Caller is expected to have already validated relativePath via
// api.validateRelativePath. This function performs a final defensive check
// for NUL bytes / leading slash / control chars so a bug in the caller
// can't smuggle a malformed key into MinIO.
func ObjectKey(userID, watchedPathID int64, relativePath string) (string, error) {
	if relativePath == "" {
		return "", fmt.Errorf("relative path is empty")
	}
	if strings.ContainsRune(relativePath, 0) {
		return "", fmt.Errorf("relative path contains NUL byte")
	}
	if strings.HasPrefix(relativePath, "/") {
		return "", fmt.Errorf("relative path must not start with /")
	}
	for _, r := range relativePath {
		if r < 0x20 { // control characters
			return "", fmt.Errorf("relative path contains control character")
		}
	}
	return fmt.Sprintf("%d/%d/%s", userID, watchedPathID, relativePath), nil
}

// VersionedObjectKey returns a per-version object key for a backed-up file.
// Format: "{userID}/{watchedPathID}/v{version}/{relativePath}".
// Kept for reading legacy version objects that predate content-addressable storage.
func VersionedObjectKey(userID, watchedPathID, version int64, relativePath string) (string, error) {
	if _, err := ObjectKey(userID, watchedPathID, relativePath); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d/%d/v%d/%s", userID, watchedPathID, version, relativePath), nil
}

// BlobKey returns the content-addressable object key for a version's bytes.
// Format: "{userID}/blobs/{checksumSHA256}".
// Multiple versions (across files and folders) with identical content share one
// object. Scoped per user so DeleteUserObjects cleans them up on account deletion.
func BlobKey(userID int64, checksumSHA256 string) string {
	return fmt.Sprintf("%d/blobs/%s", userID, checksumSHA256)
}

// IsBlobKey reports whether key is a content-addressable blob key for userID.
func IsBlobKey(userID int64, key string) bool {
	return strings.HasPrefix(key, fmt.Sprintf("%d/blobs/", userID))
}

// DeltaKey returns the object key for a binary delta (bsdiff patch) stored for
// a specific version. Format: "{userID}/deltas/{versionID}".
// Delta objects are version-scoped (not content-addressed) because a patch is
// only meaningful relative to its specific base version.
func DeltaKey(userID, versionID int64) string {
	return fmt.Sprintf("%d/deltas/%d", userID, versionID)
}

// IsDeltaKey reports whether key is a delta object key for userID.
func IsDeltaKey(userID int64, key string) bool {
	return strings.HasPrefix(key, fmt.Sprintf("%d/deltas/", userID))
}

func (c *Client) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("putting object %q: %w", key, err)
	}
	return nil
}

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

func (c *Client) ObjectExists(ctx context.Context, key string) (bool, error) {
	_, err := c.mc.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, fmt.Errorf("stat object %q: %w", key, err)
	}
	return true, nil
}

func (c *Client) CopyObject(ctx context.Context, srcKey, dstKey string) error {
	src := minio.CopySrcOptions{Bucket: c.bucket, Object: srcKey}
	dst := minio.CopyDestOptions{Bucket: c.bucket, Object: dstKey}
	if _, err := c.mc.CopyObject(ctx, dst, src); err != nil {
		return fmt.Errorf("copying object %q → %q: %w", srcKey, dstKey, err)
	}
	return nil
}

func (c *Client) DeleteObject(ctx context.Context, key string) error {
	err := c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("deleting object %q: %w", key, err)
	}
	return nil
}

// DeleteUserObjects removes all objects whose key starts with "{userID}/".
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
