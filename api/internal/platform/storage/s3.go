package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config adalah konfigurasi object storage S3-compatible (MinIO saat lokal,
// R2/S3 saat produksi). Kredensial selalu berasal dari environment
// (Architecture.md §6) — tidak pernah dari berkas di repo.
type S3Config struct {
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
}

// S3 adalah Store di atas object storage S3-compatible. Implementasinya
// memakai aws-sdk-go-v2 (Architecture.md §1) sehingga presigned URL benar-benar
// ditandatangani SigV4 dan berkas tidak pernah melewati API setelah tautannya
// dibuat.
type S3 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

// NewS3 membuat Store S3. Endpoint dan Bucket wajib terisi.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("S3_ENDPOINT dan S3_BUCKET wajib diisi untuk storage S3")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	if _, err := url.Parse(cfg.Endpoint); err != nil {
		return nil, fmt.Errorf("S3_ENDPOINT tidak valid: %w", err)
	}

	awsCfg := aws.Config{Region: region}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		awsCfg.Credentials = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		return nil, errors.New("S3_ACCESS_KEY dan S3_SECRET_KEY wajib diisi")
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = cfg.UsePathStyle
	})
	return &S3{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket}, nil
}

// Put menyimpan objek. Size (bila diketahui) dipakai sebagai Content-Length
// supaya unggahan tidak perlu di-buffer SDK.
func (s *S3) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
	}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("put objek %s: %w", key, err)
	}
	return nil
}

// Get mengembalikan pembaca objek (dipakai job latar/verifikasi AI, bukan
// dijalur unduh pengguna).
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("get objek %s: %w", key, err)
	}
	return out.Body, nil
}

// PresignGet membuat tautan baca berumur pendek.
func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return s.presignKey(ctx, key, "", ttl)
}

// PresignDownload membuat tautan baca berumur pendek yang memaksa browser
// MENGUNDUH dengan nama berkas asli (Content-Disposition: attachment).
// Lihat payment.DownloadSigner.
func (s *S3) PresignDownload(ctx context.Context, key, namaFile string, ttl time.Duration) (string, error) {
	return s.presignKey(ctx, key, namaFile, ttl)
}

func (s *S3) presignKey(ctx context.Context, key, namaFile string, ttl time.Duration) (string, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if namaFile != "" {
		in.ResponseContentDisposition = aws.String(`attachment; filename="` + sanitasiNamaHeader(namaFile) + `"`)
	}
	out, err := s.presign.PresignGetObject(ctx, in, func(o *s3.PresignOptions) {
		o.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("presign objek %s: %w", key, err)
	}
	return out.URL, nil
}

// Delete menghapus objek (dipakai membersihkan objek yatim bila pencatatan
// metadata gagal).
func (s *S3) Delete(ctx context.Context, key string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete objek %s: %w", key, err)
	}
	return nil
}

// Health memeriksa bucket bisa diakses.
func (s *S3) Health(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("head bucket %s: %w", s.bucket, err)
	}
	return nil
}

// List mendaftarkan seluruh objek dengan awalan prefix (rekursif) memakai
// ListObjectsV2 berhalaman. Dipakai sinkronisasi cadangan dan rekonsiliasi
// DB↔object storage; ia hanya membaca metadata (bukan isi objek), sehingga
// biayanya tetap kecil walau bucket besar.
func (s *S3) List(ctx context.Context, prefix string) ([]Info, error) {
	out := []Info{}
	var token *string
	for {
		in := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)}
		if token != nil {
			in.ContinuationToken = token
		}
		page, err := s.client.ListObjectsV2(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("list objek %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			info := Info{Key: aws.ToString(obj.Key), Size: aws.ToInt64(obj.Size)}
			if obj.LastModified != nil {
				info.ModTime = *obj.LastModified
			}
			out = append(out, info)
		}
		if page.IsTruncated == nil || !*page.IsTruncated || page.NextContinuationToken == nil {
			break
		}
		token = page.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// sanitasiNamaHeader membuang karakter yang bisa memecah header HTTP.
func sanitasiNamaHeader(nama string) string {
	out := make([]rune, 0, len(nama))
	for _, r := range nama {
		switch {
		case r < 0x20 || r == 0x7f || r == '"' || r == '\\':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
