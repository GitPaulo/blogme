// Package blob wraps the Azure Blob Storage client with the few operations this app
// needs, authenticating with the function app's managed identity.
//
// SDK reference: https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/storage/azblob
package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

// ErrNotFound is returned when a blob does not exist.
var ErrNotFound = errors.New("blob not found")

type Client struct {
	svc *azblob.Client
}

func New(accountName string) (*Client, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("credential: %w", err)
	}

	serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/", accountName)
	svc, err := azblob.NewClient(serviceURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("blob client: %w", err)
	}

	return &Client{svc: svc}, nil
}

// Download returns the blob contents along with its current ETag.
func (c *Client) Download(ctx context.Context, container, name string) ([]byte, string, error) {
	resp, err := c.svc.DownloadStream(ctx, container, name, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("download %s/%s: %w", container, name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read %s/%s: %w", container, name, err)
	}

	var etag string
	if resp.ETag != nil {
		etag = string(*resp.ETag)
	}
	return data, etag, nil
}

// DownloadString is a convenience wrapper for small text blobs such as cursors.
func (c *Client) DownloadString(ctx context.Context, container, name string) (string, error) {
	data, _, err := c.Download(ctx, container, name)
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(data)), nil
}

// ETag returns the blob's current ETag without transferring its contents.
func (c *Client) ETag(ctx context.Context, container, name string) (string, error) {
	props, err := c.svc.ServiceClient().
		NewContainerClient(container).NewBlobClient(name).
		GetProperties(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("properties %s/%s: %w", container, name, err)
	}
	if props.ETag == nil {
		return "", nil
	}
	return string(*props.ETag), nil
}

func (c *Client) Upload(ctx context.Context, container, name string, data []byte) error {
	if _, err := c.svc.UploadBuffer(ctx, container, name, data, nil); err != nil {
		return fmt.Errorf("upload %s/%s: %w", container, name, err)
	}
	return nil
}
