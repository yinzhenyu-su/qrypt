package s3

import (
	"context"
	stdpath "path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) awsConfig() aws.Config {
	cfg := aws.Config{
		Region:      d.region,
		Credentials: credentials.NewStaticCredentialsProvider(d.accessKey, d.secretKey, d.sessionToken),
	}
	return cfg
}

func (d *Driver) toS3Dir(id string) string {
	key := d.toS3Key(id)
	if key == "" {
		return ""
	}
	if strings.HasSuffix(key, "/") {
		return key
	}
	return key + "/"
}

func (d *Driver) listV1(ctx context.Context, parentID string) ([]drive.Entry, error) {
	prefix := d.toS3Dir(parentID)
	parentID = d.normParent(parentID)
	entries := make([]drive.Entry, 0)
	marker := ""
	for {
		input := &s3.ListObjectsInput{
			Bucket:    aws.String(d.bucket),
			Prefix:    aws.String(prefix),
			Marker:    aws.String(marker),
			Delimiter: aws.String("/"),
		}
		output, err := d.client.ListObjects(ctx, input)
		if err != nil {
			return nil, err
		}

		for _, cp := range output.CommonPrefixes {
			relKey := d.relPath(strings.TrimRight(aws.ToString(cp.Prefix), "/"))
			if relKey == "" {
				continue
			}
			name := stdpath.Base(relKey)
			entries = append(entries, drive.Entry{
				ID:       stdpath.Join(parentID, name) + "/",
				ParentID: parentID,
				Name:     name,
				IsDir:    true,
			})
		}

		for _, obj := range output.Contents {
			s3Key := aws.ToString(obj.Key)
			relKey := d.relPath(s3Key)
			if relKey == "" {
				continue
			}
			name := stdpath.Base(relKey)
			if name == d.placeholder {
				continue
			}
			if strings.HasSuffix(relKey, "/") {
				continue
			}
			entries = append(entries, drive.Entry{
				ID:       stdpath.Join(parentID, name),
				ParentID: parentID,
				Name:     name,
				Size:     aws.ToInt64(obj.Size),
				ModTime:  aws.ToTime(obj.LastModified),
			})
		}

		if !aws.ToBool(output.IsTruncated) {
			break
		}
		marker = aws.ToString(output.NextMarker)
		if marker == "" && len(output.Contents) > 0 {
			marker = aws.ToString(output.Contents[len(output.Contents)-1].Key)
		}
	}
	return entries, nil
}

func (d *Driver) listV2(ctx context.Context, parentID string) ([]drive.Entry, error) {
	prefix := d.toS3Dir(parentID)
	parentID = d.normParent(parentID)
	entries := make([]drive.Entry, 0)
	var continuationToken *string
	for {
		input := &s3.ListObjectsV2Input{
			Bucket:            aws.String(d.bucket),
			Prefix:            aws.String(prefix),
			Delimiter:         aws.String("/"),
			ContinuationToken: continuationToken,
		}
		output, err := d.client.ListObjectsV2(ctx, input)
		if err != nil {
			return nil, err
		}

		for _, cp := range output.CommonPrefixes {
			relKey := d.relPath(strings.TrimRight(aws.ToString(cp.Prefix), "/"))
			if relKey == "" {
				continue
			}
			name := stdpath.Base(relKey)
			entries = append(entries, drive.Entry{
				ID:       stdpath.Join(parentID, name) + "/",
				ParentID: parentID,
				Name:     name,
				IsDir:    true,
			})
		}

		for _, obj := range output.Contents {
			s3Key := aws.ToString(obj.Key)
			relKey := d.relPath(s3Key)
			if relKey == "" {
				continue
			}
			name := stdpath.Base(relKey)
			if name == d.placeholder {
				continue
			}
			if strings.HasSuffix(relKey, "/") {
				continue
			}
			entries = append(entries, drive.Entry{
				ID:       stdpath.Join(parentID, name),
				ParentID: parentID,
				Name:     name,
				Size:     aws.ToInt64(obj.Size),
				ModTime:  aws.ToTime(obj.LastModified),
			})
		}

		if !aws.ToBool(output.IsTruncated) {
			break
		}
		continuationToken = output.NextContinuationToken
	}
	return entries, nil
}
