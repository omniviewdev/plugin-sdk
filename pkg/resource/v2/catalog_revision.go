package resourcev2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"

	"github.com/omniviewdev/plugin-sdk/internal/catalogadmission"
)

// MaxCatalogRevisionBytes bounds trusted admission context and its wire encoding.
const MaxCatalogRevisionBytes = 4096

type (
	catalogRevisionKey struct{}
	catalogSlotKey     struct{}
)

// WithCatalogRevision pins the exact catalog observation accepted by the caller.
// A revision conveys admission context, not permission or authority.
func WithCatalogRevision(ctx context.Context, revision string) context.Context {
	return context.WithValue(ctx, catalogRevisionKey{}, revision)
}

// CatalogRevisionFromContext returns the default observation and its presence.
// Presence does not validate syntax or authorize dispatch; route-qualified lookup is separate.
func CatalogRevisionFromContext(ctx context.Context) (string, bool) {
	revision, ok := ctx.Value(catalogRevisionKey{}).(string)
	return revision, ok
}

// ValidateCatalogRevision checks bounded observation syntax, not authorization.
func ValidateCatalogRevision(revision string) error {
	if violation := catalogadmission.ValidateRevision(revision, MaxCatalogRevisionBytes); violation != nil {
		return NewError(violation.Code, violation.Message)
	}
	return nil
}

// CheckCatalogRevision rejects stale or absent observations before dispatch.
func CheckCatalogRevision(ctx context.Context, catalog Catalog) error {
	revision, _ := CatalogRevisionFromContext(ctx)
	if err := ValidateCatalogRevision(revision); err != nil {
		return err
	}
	if revision != catalog.Revision {
		return NewError("RESET_REQUIRED", "The accepted catalog revision is no longer current.")
	}
	return nil
}

func newCatalogNonce() ([32]byte, error) {
	var nonce [32]byte
	_, err := rand.Read(nonce[:])
	return nonce, err
}

// NewCatalogBindingEpoch issues an opaque native-source binding epoch.
// Custom providers retain it for one actual source/configuration generation
// and rotate it when that binding changes, not when native catalog data changes.
// A fixed stateless provider issues one epoch per provider instance.
func NewCatalogBindingEpoch() (string, error) {
	nonce, err := newCatalogNonce()
	if err != nil {
		return "", err
	}
	return "ovce1." + base64.RawURLEncoding.EncodeToString(nonce[:]), nil
}

// BindCatalogRevision frames a native catalog revision under its actual source
// binding. The provider must own the epoch's lifetime; servers never invent one.
func BindCatalogRevision(bindingEpoch, nativeRevision string) (string, error) {
	if err := ValidateCatalogRevision(bindingEpoch); err != nil {
		return "", err
	}
	if err := text(nativeRevision, true); err != nil {
		return "", err
	}
	parts := strings.Split(bindingEpoch, ".")
	if len(parts) != 2 || parts[0] != "ovce1" {
		return "", NewError("RESET_REQUIRED", "Invalid SDK catalog binding epoch.")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != parts[1] {
		return "", NewError("RESET_REQUIRED", "Invalid SDK catalog binding epoch.")
	}
	var nonce [32]byte
	copy(nonce[:], decoded)
	return boundCatalogRevision(nonce, nativeRevision), nil
}

func boundCatalogRevision(nonce [32]byte, revision string) string {
	h := sha256.New()
	h.Write([]byte("omniview.resource.catalog.slot.v1\x00"))
	h.Write(nonce[:])
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(revision)))
	h.Write(size[:])
	h.Write([]byte(revision))
	return "ovc1." + base64.RawURLEncoding.EncodeToString(
		nonce[:],
	) + "." + base64.RawURLEncoding.EncodeToString(
		h.Sum(nil),
	)
}

// CatalogBindingEpoch returns the opaque client-slot epoch of an SDK-owned
// catalog revision. Native catalog changes do not change this epoch; replacing
// the native client always does. Callers must not parse revision tokens.
func CatalogBindingEpoch(revision string) (string, error) {
	if err := ValidateCatalogRevision(revision); err != nil {
		return "", err
	}
	parts := strings.Split(revision, ".")
	if len(parts) != 3 || parts[0] != "ovc1" {
		return "", NewError("RESET_REQUIRED", "Catalog revision has no SDK client binding.")
	}
	for _, part := range parts[1:] {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(part)
		if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != part {
			return "", NewError("RESET_REQUIRED", "Catalog revision has an invalid SDK client binding.")
		}
	}
	return "ovce1." + parts[1], nil
}
