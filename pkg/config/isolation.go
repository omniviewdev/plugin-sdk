package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

const (
	// StateIsolationCapability identifies the SDK's per-profile state-root isolation contract.
	StateIsolationCapability = "state-root-v1"
	// StateIsolationSDKModule identifies the module whose revision the trusted builder attests.
	StateIsolationSDKModule  = "github.com/omniviewdev/plugin-sdk"
)

// StateIsolationProvenance is a trusted builder's attestation, not a declaration
// an arbitrary plugin may use to grant itself permission to start. It is checked
// without executing the artifact. Absence means unsupported, not legacy-safe.
// The host must establish the attestation's trusted origin separately.
// No lifecycle RPC/protocol change is required to inspect this metadata.
type StateIsolationProvenance struct {
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	Capability    string `json:"capability" yaml:"capability"`
	SDKModule     string `json:"sdk_module" yaml:"sdk_module"`
	SDKRevision   string `json:"sdk_revision" yaml:"sdk_revision"`
	// SDKSourceSHA256 identifies the trusted builder's exact selected working-tree
	// snapshot, permitting live feature edits above the reviewed capability base.
	// Empty is reserved for immutable, clean, commit-only trusted builds.
	SDKSourceSHA256 string `json:"sdk_source_sha256,omitempty" yaml:"sdk_source_sha256,omitempty"`
	BinarySHA256    string `json:"binary_sha256" yaml:"binary_sha256"`
}

// BuildStateIsolationProvenance is called only by a trusted build pipeline after
// validating the selected SDK module/source and known-fixed capability base.
// For editable sources the caller supplies the same stable source snapshot digest
// observed before and after building. Hashes do NOT prove SDK source inclusion.
func BuildStateIsolationProvenance(binaryPath, sdkRevision, sdkSourceSHA256 string) (*StateIsolationProvenance, error) {
	if !validRevision(sdkRevision) {
		return nil, fmt.Errorf("SDK provenance requires a resolved lowercase commit hash")
	}
	if !validSourceSHA256(sdkSourceSHA256) {
		return nil, fmt.Errorf("SDK source snapshot requires a lowercase SHA-256 digest")
	}
	digest, err := artifactDigest(binaryPath)
	if err != nil {
		return nil, err
	}
	return &StateIsolationProvenance{
		SchemaVersion:   1,
		Capability:      StateIsolationCapability,
		SDKModule:       StateIsolationSDKModule,
		SDKRevision:     sdkRevision,
		SDKSourceSHA256: sdkSourceSHA256,
		BinarySHA256:    digest,
	}, nil
}

// VerifyArtifact binds a trusted attestation to artifact bytes, the host's
// expected known-fixed SDK capability base, and its expected source snapshot.
// An empty expected snapshot accepts only a commit-only attestation; source-bound
// attestations cannot downgrade. It never starts the plugin, authenticates a
// registry/source, proves source inclusion, or trusts plugin.yaml.
func (p *StateIsolationProvenance) VerifyArtifact(
	binaryPath, expectedSDKRevision, expectedSDKSourceSHA256 string,
) error {
	if p == nil || p.SchemaVersion != 1 || p.Capability != StateIsolationCapability ||
		p.SDKModule != StateIsolationSDKModule {
		return fmt.Errorf("artifact has no supported state-root isolation attestation")
	}
	if !validRevision(expectedSDKRevision) || p.SDKRevision != expectedSDKRevision {
		return fmt.Errorf("artifact SDK revision does not match trusted fixed SDK revision")
	}
	if !validSourceSHA256(expectedSDKSourceSHA256) || p.SDKSourceSHA256 != expectedSDKSourceSHA256 {
		return fmt.Errorf("artifact SDK source snapshot does not match trusted source digest")
	}
	if len(p.BinarySHA256) != sha256.Size*2 {
		return fmt.Errorf("artifact has invalid SHA-256 provenance")
	}
	digest, err := artifactDigest(binaryPath)
	if err != nil {
		return err
	}
	if p.BinarySHA256 != digest {
		return fmt.Errorf("artifact digest differs from isolation attestation")
	}
	return nil
}

func validRevision(revision string) bool {
	return (len(revision) == 40 || len(revision) == 64) && validLowerHex(revision)
}

func validSourceSHA256(digest string) bool {
	return digest == "" || len(digest) == sha256.Size*2 && validLowerHex(digest)
}

func validLowerHex(value string) bool {
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func artifactDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact must be a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
