package build

import (
	"context"
	"fmt"
	"mime"
	"net/url"
	"path"
	"path/filepath"

	"github.com/jourloy/kilnbridge/internal/model"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Manifest is the discovery marker: upload it only after every artifact and
// checksum has succeeded. Retrying the same job overwrites identical objects.
func Upload(ctx context.Context, out, id string, s model.Spec, artifacts []model.Artifact, log func(string)) error {
	u, err := url.Parse(s.Storage.Endpoint)
	if err != nil {
		return err
	}
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(s.Storage.AccessKey, s.Storage.SecretKey, s.Storage.SessionToken), Secure: true, Region: s.Storage.Region, BucketLookup: minio.BucketLookupPath})
	if err != nil {
		return err
	}
	return uploadFiles(ctx, out, id, s, artifacts, log, func(name, local, contentType string) error {
		_, e := client.FPutObject(ctx, s.Storage.Bucket, name, local, minio.PutObjectOptions{ContentType: contentType, CacheControl: "private, max-age=0"})
		return e
	})
}

func uploadFiles(ctx context.Context, out, id string, s model.Spec, artifacts []model.Artifact, log func(string), put func(string, string, string) error) error {
	names := make([]string, 0, len(artifacts)+2)
	for _, a := range artifacts {
		if !model.SafeRelativeName(a.Name) || model.ArtifactMetadataPath(a.Name) {
			return fmt.Errorf("invalid artifact name: %q", a.Name)
		}
		digest, e := FileSHA256(filepath.Join(out, filepath.FromSlash(a.Name)))
		if e != nil {
			return e
		}
		if digest != a.SHA256 {
			return fmt.Errorf("artifact %s changed after packaging", a.Name)
		}
		names = append(names, a.Name)
	}
	names = append(names, "SHA256SUMS.txt", "manifest.json")
	for _, name := range names {
		if log != nil {
			log("Uploading " + name)
		}
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if name == "manifest.json" {
			contentType = "application/json"
		}
		if name == "SHA256SUMS.txt" {
			contentType = "text/plain"
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := put(path.Join(s.Storage.Prefix, id, name), filepath.Join(out, filepath.FromSlash(name)), contentType); err != nil {
			return err
		}
	}
	return nil
}
