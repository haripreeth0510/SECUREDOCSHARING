// Package keys wraps and unwraps per-document data encryption keys (DEKs)
// with a key encryption key (KEK). In Azure the KEK lives in Key Vault and
// never leaves it; locally a KEK from the environment stands in for it.
package keys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
)

// DEKSize is the AES-256 data key length.
const DEKSize = 32

var (
	ErrUnknownKEK = errors.New("document was wrapped with a key this service does not hold")
	ErrInvalidDEK = errors.New("data key must be 32 bytes")
)

// KeyWrapper is what the document service needs from key management.
type KeyWrapper interface {
	// Wrap encrypts dek and returns the wrapped bytes plus the ID of the KEK
	// version used, which must be stored next to the wrapped key.
	Wrap(ctx context.Context, dek []byte) (wrapped []byte, kekID string, err error)
	// Unwrap reverses Wrap. kekID selects the KEK version, so documents
	// wrapped before a key rotation still decrypt.
	Unwrap(ctx context.Context, wrapped []byte, kekID string) ([]byte, error)
	// Ping checks that the KEK is reachable with the current credentials.
	Ping(ctx context.Context) error
}

// NewDEK returns a fresh random AES-256 key for one document.
func NewDEK() ([]byte, error) {
	k := make([]byte, DEKSize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("generate data key: %w", err)
	}
	return k, nil
}

// ---------------------------------------------------------------------------
// Azure Key Vault
// ---------------------------------------------------------------------------

type keyVault struct {
	client  *azkeys.Client
	keyName string
}

// NewKeyVault wraps with RSA-OAEP-256 inside Key Vault. It authenticates via
// DefaultAzureCredential (Managed Identity in Azure, `az login` locally).
// The app identity needs only the "Key Vault Crypto User" role.
func NewKeyVault(vaultURL, keyName string) (KeyWrapper, error) {
	if vaultURL == "" || keyName == "" {
		return nil, errors.New("keys: vault URL and key name are required")
	}
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("keys: credential: %w", err)
	}
	client, err := azkeys.NewClient(vaultURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("keys: client: %w", err)
	}
	return &keyVault{client: client, keyName: keyName}, nil
}

func (k *keyVault) Wrap(ctx context.Context, dek []byte) ([]byte, string, error) {
	if len(dek) != DEKSize {
		return nil, "", ErrInvalidDEK
	}
	// Empty version = the current key version. The returned KID pins it.
	resp, err := k.client.WrapKey(ctx, k.keyName, "", azkeys.KeyOperationParameters{
		Algorithm: to.Ptr(azkeys.EncryptionAlgorithmRSAOAEP256),
		Value:     dek,
	}, nil)
	if err != nil {
		return nil, "", fmt.Errorf("keys: wrap: %w", err)
	}
	if resp.KID == nil || resp.KID.Version() == "" {
		return nil, "", errors.New("keys: wrap: Key Vault returned no key version")
	}
	return resp.Result, string(*resp.KID), nil
}

func (k *keyVault) Unwrap(ctx context.Context, wrapped []byte, kekID string) ([]byte, error) {
	id := azkeys.ID(kekID)
	// Only our configured key is used, whatever the stored ID says.
	if id.Name() != k.keyName || id.Version() == "" {
		return nil, ErrUnknownKEK
	}
	resp, err := k.client.UnwrapKey(ctx, k.keyName, id.Version(), azkeys.KeyOperationParameters{
		Algorithm: to.Ptr(azkeys.EncryptionAlgorithmRSAOAEP256),
		Value:     wrapped,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("keys: unwrap: %w", err)
	}
	if len(resp.Result) != DEKSize {
		return nil, ErrInvalidDEK
	}
	return resp.Result, nil
}

func (k *keyVault) Ping(ctx context.Context) error {
	if _, err := k.client.GetKey(ctx, k.keyName, "", nil); err != nil {
		return fmt.Errorf("keys: key %q unreachable: %w", k.keyName, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Local development KEK
// ---------------------------------------------------------------------------

type localKEK struct {
	aead cipher.AEAD
	id   string
}

// NewLocal wraps DEKs with AES-256-GCM under a 32-byte KEK held in memory.
// It is for local development and tests only; main refuses it outside them.
func NewLocal(kek []byte) (KeyWrapper, error) {
	if len(kek) != 32 {
		return nil, errors.New("keys: local KEK must be 32 bytes")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// The ID is a fingerprint, so a changed LOCAL_KEK is detected instead of
	// producing garbage.
	sum := sha256.Sum256(kek)
	return &localKEK{aead: aead, id: "local:" + hex.EncodeToString(sum[:8])}, nil
}

func (l *localKEK) Wrap(_ context.Context, dek []byte) ([]byte, string, error) {
	if len(dek) != DEKSize {
		return nil, "", ErrInvalidDEK
	}
	nonce := make([]byte, l.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	// The KEK ID is bound as associated data.
	return l.aead.Seal(nonce, nonce, dek, []byte(l.id)), l.id, nil
}

func (l *localKEK) Unwrap(_ context.Context, wrapped []byte, kekID string) ([]byte, error) {
	if kekID != l.id || !strings.HasPrefix(kekID, "local:") {
		return nil, ErrUnknownKEK
	}
	ns := l.aead.NonceSize()
	if len(wrapped) < ns+l.aead.Overhead() {
		return nil, ErrInvalidDEK
	}
	dek, err := l.aead.Open(nil, wrapped[:ns], wrapped[ns:], []byte(kekID))
	if err != nil {
		return nil, fmt.Errorf("keys: unwrap: %w", err)
	}
	return dek, nil
}

func (l *localKEK) Ping(context.Context) error { return nil }
