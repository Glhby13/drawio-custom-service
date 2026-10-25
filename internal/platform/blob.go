// Package platform is the drawio-custom-service "platform" deployable: documents, authorization (PDP), the
// sequenced operation log, snapshots/versions, realtime collaboration, comments and audit (ADR-0005).
package platform

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Glhby13/drawio-custom-service/internal/config"
)

// Blob stores snapshots in S3 (SeaweedFS in the lab). Objects are gzip'd JSON/XML.
type Blob struct {
	c      *minio.Client
	bucket string
}

// NewBlob connects (path-style, static credentials).
func NewBlob(cfg config.Platform) (*Blob, error) {
	opts := &minio.Options{Creds: credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""), Secure: cfg.S3UseSSL, Region: cfg.S3Region, BucketLookup: minio.BucketLookupPath}
	if cfg.S3UseSSL && cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("EXTRA_CA_FILE holds no certificate")
		}
		opts.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 20 * time.Second}
	}
	c, err := minio.New(cfg.S3Endpoint, opts)
	if err != nil {
		return nil, err
	}
	return &Blob{c: c, bucket: cfg.S3Bucket}, nil
}

// Put stores data gzip-compressed.
func (b *Blob) Put(ctx context.Context, key string, data []byte) (int, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	n := buf.Len()
	_, err := b.c.PutObject(ctx, b.bucket, key, &buf, int64(n), minio.PutObjectOptions{ContentType: "application/gzip"})
	return n, err
}

// Get returns the decompressed object.
func (b *Blob) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := b.c.GetObject(ctx, b.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = o.Close() }()
	zr, err := gzip.NewReader(o)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(zr, 256<<20))
}

// Ping checks the bucket is reachable with the configured identity.
func (b *Blob) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for o := range b.c.ListObjects(ctx, b.bucket, minio.ListObjectsOptions{MaxKeys: 1}) {
		return o.Err
	}
	return nil
}
