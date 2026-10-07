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
	azcontainer "github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
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

// List returns the names of the blobs directly under prefix, reading delimiter as a
// path separator, and reports whether it read them all. It stops after maxPages pages,
// each one billable List Blobs operation of up to 5,000 entries; a name that runs on
// past another delimiter is folded into a single entry and not returned.
// see: https://learn.microsoft.com/en-us/rest/api/storageservices/enumerating-blob-resources
func (c *Client) List(ctx context.Context, container, prefix, delimiter string, maxPages int) ([]string, bool, error) {
	pager := c.svc.ServiceClient().NewContainerClient(container).
		NewListBlobsHierarchyPager(delimiter, &azcontainer.ListBlobsHierarchyOptions{Prefix: &prefix})

	var names []string
	for range maxPages {
		if !pager.More() {
			break
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			if bloberror.HasCode(err, bloberror.ContainerNotFound) {
				return nil, false, ErrNotFound
			}
			return nil, false, fmt.Errorf("list %s/%s: %w", container, prefix, err)
		}
		if page.Segment == nil {
			continue
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name != nil {
				names = append(names, *item.Name)
			}
		}
	}
	return names, !pager.More(), nil
}

// Delete removes a blob. Delete operations are not billed.
// see: https://azure.microsoft.com/en-gb/pricing/details/storage/blobs/
func (c *Client) Delete(ctx context.Context, container, name string) error {
	if _, err := c.svc.DeleteBlob(ctx, container, name, nil); err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete %s/%s: %w", container, name, err)
	}
	return nil
}

func (c *Client) Upload(ctx context.Context, container, name string, data []byte) error {
	if _, err := c.svc.UploadBuffer(ctx, container, name, data, nil); err != nil {
		return fmt.Errorf("upload %s/%s: %w", container, name, err)
	}
	return nil
}
