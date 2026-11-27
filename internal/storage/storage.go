// Package storage stores encrypted document blobs in Azure Blob Storage.
// It only ever sees ciphertext: encryption happens before Put and after Get.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
)

var (
	ErrNotFound      = errors.New("blob not found")
	ErrAlreadyExists = errors.New("blob already exists")
)

// BlobStore is the storage the document service depends on.
type BlobStore interface {
	// Put uploads r under name. It never overwrites: an existing name fails
	// with ErrAlreadyExists, so a bug cannot silently replace a document.
	Put(ctx context.Context, name string, r io.Reader) error
	// Get streams a blob. The caller must close the reader.
	Get(ctx context.Context, name string) (io.ReadCloser, error)
	// Delete removes a blob and its snapshots. Deleting a missing blob is not an error.
	Delete(ctx context.Context, name string) error
	// Ping checks that the container is reachable with the current credentials.
	Ping(ctx context.Context) error
}

// Config selects how to reach Blob Storage. Exactly one of ConnectionString
// (local Azurite emulator) or AccountURL (real Azure, keyless) must be set.
type Config struct {
	ConnectionString string
	AccountURL       string // e.g. https://<account>.blob.core.windows.net/
	Container        string
}

type azureStore struct {
	client    *azblob.Client
	container string
}

// NewAzure builds a store. With AccountURL it authenticates through
// DefaultAzureCredential: Managed Identity in Azure, `az login` locally. No
// storage account keys are used, in line with the technology decisions.
func NewAzure(cfg Config) (BlobStore, error) {
	if cfg.Container == "" {
		return nil, errors.New("storage: container name is required")
	}
	var (
		client *azblob.Client
		err    error
	)
	switch {
	case cfg.ConnectionString != "" && cfg.AccountURL != "":
		return nil, errors.New("storage: set either a connection string or an account URL, not both")
	case cfg.ConnectionString != "":
		client, err = azblob.NewClientFromConnectionString(cfg.ConnectionString, nil)
	case cfg.AccountURL != "":
		var cred azcore.TokenCredential
		cred, err = azidentity.NewDefaultAzureCredential(nil)
		if err == nil {
			client, err = azblob.NewClient(cfg.AccountURL, cred, nil)
		}
	default:
		return nil, errors.New("storage: no connection string or account URL configured")
	}
	if err != nil {
		return nil, fmt.Errorf("storage: create client: %w", err)
	}
	return &azureStore{client: client, container: cfg.Container}, nil
}

func (s *azureStore) Put(ctx context.Context, name string, r io.Reader) error {
	_, err := s.client.UploadStream(ctx, s.container, name, r, &blockblob.UploadStreamOptions{
		BlockSize:   4 << 20, // 4 MiB blocks
		Concurrency: 4,       // caps memory per upload at about 16 MiB
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: to.Ptr("application/octet-stream")},
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{
			IfNoneMatch: to.Ptr(azcore.ETagAny), // fail if the blob exists
		}},
	})
	if bloberror.HasCode(err, bloberror.BlobAlreadyExists, bloberror.ConditionNotMet) {
		return fmt.Errorf("storage: put %s: %w", name, ErrAlreadyExists)
	}
	if err != nil {
		return fmt.Errorf("storage: put %s: %w", name, err)
	}
	return nil
}

func (s *azureStore) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := s.client.DownloadStream(ctx, s.container, name, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return nil, fmt.Errorf("storage: get %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("storage: get %s: %w", name, err)
	}
	return resp.Body, nil
}

func (s *azureStore) Delete(ctx context.Context, name string) error {
	_, err := s.client.DeleteBlob(ctx, s.container, name, &azblob.DeleteBlobOptions{
		DeleteSnapshots: to.Ptr(azblob.DeleteSnapshotsOptionTypeInclude),
	})
	if err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return fmt.Errorf("storage: delete %s: %w", name, err)
	}
	return nil
}

func (s *azureStore) Ping(ctx context.Context) error {
	_, err := s.client.ServiceClient().NewContainerClient(s.container).GetProperties(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: container %q unreachable: %w", s.container, err)
	}
	return nil
}

// azuriteAccountKey is the fixed, publicly documented key of the Azurite
// emulator's built-in account. It is not a secret and only works against Azurite.
const azuriteAccountKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

// AzuriteConnectionString builds the emulator connection string from its blob
// endpoint, e.g. http://127.0.0.1:10000/devstoreaccount1. Keeping it out of
// .env avoids a value with semicolons, which shells split when sourcing.
func AzuriteConnectionString(blobEndpoint string) string {
	return "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=" +
		azuriteAccountKey + ";BlobEndpoint=" + blobEndpoint + ";"
}

// EnsureContainer creates the container if it is missing. Only for local
// development and tests: in Azure the container is provisioned by
// scripts/azure-setup.sh, and the app identity has no rights to create one.
func EnsureContainer(ctx context.Context, cfg Config) error {
	if cfg.ConnectionString == "" {
		return errors.New("storage: EnsureContainer is only for the local emulator")
	}
	client, err := azblob.NewClientFromConnectionString(cfg.ConnectionString, nil)
	if err != nil {
		return err
	}
	_, err = client.CreateContainer(ctx, cfg.Container, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		return fmt.Errorf("storage: create container: %w", err)
	}
	return nil
}
