package blob

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 is an S3-compatible Store built on minio-go. It works against MinIO
// (local docker-compose), AWS S3, and GCS's S3-interop endpoint — this is
// Backend's blob store. Keys map directly to object names within one bucket
// (the "/"-separated logical key IS the object key), so the same
// "segments/<device>/<start>-<end>.parquet" layout Edge writes on disk becomes
// the object key in the cloud, and Edge→Backend sync is a plain object upload.
type S3 struct {
	client *minio.Client
	bucket string
}

// S3Config configures an S3 store. Endpoint is host:port without scheme
// (e.g. "localhost:9000"); UseSSL selects https. AccessKey/SecretKey are the
// static credentials (MinIO root creds locally; IAM/HMAC keys in cloud).
type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Region    string
}

// NewS3 dials an S3-compatible endpoint and ensures the bucket exists. It does
// not create credentials or policies; those are provisioned out of band.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("blob: s3 config needs endpoint and bucket")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("blob: s3 new client: %w", err)
	}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("blob: s3 bucket exists: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("blob: s3 make bucket %q: %w", cfg.Bucket, err)
		}
	}
	return &S3{client: client, bucket: cfg.Bucket}, nil
}

// NewS3WithClient wraps an already-constructed minio client (used by tests that
// share a client, and by callers that manage their own credential chain). The
// bucket is assumed to exist.
func NewS3WithClient(client *minio.Client, bucket string) *S3 {
	return &S3{client: client, bucket: bucket}
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader) error {
	clean, err := cleanKey(key)
	if err != nil {
		return err
	}
	// Size -1 streams with multipart; fine for segment files (a few MB each).
	_, err = s.client.PutObject(ctx, s.bucket, clean, r, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return fmt.Errorf("blob: s3 put %q: %w", key, err)
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	clean, err := cleanKey(key)
	if err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, clean, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("blob: s3 get %q: %w", key, err)
	}
	// GetObject is lazy: it does not touch the network until first Read. Stat now
	// so a missing key surfaces as ErrNotFound here rather than on first Read.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		if isS3NotFound(err) {
			return nil, fmt.Errorf("blob: s3 get %q: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("blob: s3 stat %q: %w", key, err)
	}
	return obj, nil
}

func (s *S3) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("blob: s3 list %q: %w", prefix, obj.Err)
		}
		out = append(out, ObjectInfo{Key: obj.Key, Size: obj.Size})
	}
	// minio lists in lexical order already, but sort defensively to match FS.
	return out, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	clean, err := cleanKey(key)
	if err != nil {
		return err
	}
	// S3 DeleteObject is idempotent (no error on a missing key), so probe first
	// to honour the Store contract's ErrNotFound.
	if _, err := s.client.StatObject(ctx, s.bucket, clean, minio.StatObjectOptions{}); err != nil {
		if isS3NotFound(err) {
			return fmt.Errorf("blob: s3 delete %q: %w", key, ErrNotFound)
		}
		return fmt.Errorf("blob: s3 stat %q: %w", key, err)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, clean, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("blob: s3 delete %q: %w", key, err)
	}
	return nil
}

// isS3NotFound reports whether err is minio's "key does not exist" response.
func isS3NotFound(err error) bool {
	if errors.Is(err, ErrNotFound) {
		return true
	}
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == 404
}

// Compile-time assertion.
var _ Store = (*S3)(nil)
