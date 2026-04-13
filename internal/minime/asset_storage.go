package minime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type assetStorage interface {
	PutObject(ctx context.Context, key, contentType string, payload []byte) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	SignedGetURL(ctx context.Context, key, fileName string, ttl time.Duration) (string, error)
}

type s3AssetStorage struct {
	client        *s3.Client
	presigner     *s3.PresignClient
	bucket        string
	keyPrefix     string
	objectTagging string
}

func assetStorageForConfig(config Config) (assetStorage, error) {
	if config.AssetStorage != nil {
		return config.AssetStorage, nil
	}

	mode := strings.ToLower(strings.TrimSpace(config.AssetBackend))
	if mode == "" || mode == "file" {
		return nil, nil
	}
	if mode != "s3" {
		return nil, fmt.Errorf("unsupported asset backend %q", config.AssetBackend)
	}
	if strings.TrimSpace(config.AssetBucket) == "" {
		return nil, errors.New("MINIME_ASSET_BUCKET is required when MINIME_ASSET_BACKEND=s3")
	}

	region := strings.TrimSpace(config.AssetRegion)
	if region == "" {
		region = "us-east-1"
	}

	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if endpoint := strings.TrimSpace(config.AssetEndpoint); endpoint != "" {
		loadOptions = append(loadOptions, awsconfig.WithBaseEndpoint(endpoint))
	}
	if accessKey := strings.TrimSpace(config.AssetAccessKeyID); accessKey != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			accessKey,
			strings.TrimSpace(config.AssetSecretAccessKey),
			strings.TrimSpace(config.AssetSessionToken),
		)))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = config.AssetForcePathStyle
	})

	return &s3AssetStorage{
		client:        client,
		presigner:     s3.NewPresignClient(client),
		bucket:        strings.TrimSpace(config.AssetBucket),
		keyPrefix:     strings.Trim(strings.TrimSpace(config.AssetKeyPrefix), "/"),
		objectTagging: strings.TrimSpace(config.AssetObjectTagging),
	}, nil
}

func (s *s3AssetStorage) objectKey(key string) string {
	cleanKey := strings.Trim(strings.TrimSpace(key), "/")
	if s.keyPrefix == "" {
		return cleanKey
	}
	return s.keyPrefix + "/" + cleanKey
}

func (s *s3AssetStorage) PutObject(ctx context.Context, key, contentType string, payload []byte) error {
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
		Body:   bytes.NewReader(payload),
	}
	if strings.TrimSpace(contentType) != "" {
		input.ContentType = aws.String(contentType)
	}
	if s.objectTagging != "" {
		input.Tagging = aws.String(s.objectTagging)
	}

	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (s *s3AssetStorage) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
	})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	return result.Body, nil
}

func (s *s3AssetStorage) SignedGetURL(ctx context.Context, key, fileName string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	contentDisposition := fmt.Sprintf(`inline; filename="%s"`, strings.ReplaceAll(fileName, `"`, ""))
	result, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(s.bucket),
		Key:                        aws.String(s.objectKey(key)),
		ResponseContentDisposition: aws.String(contentDisposition),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign get object: %w", err)
	}
	return result.URL, nil
}
