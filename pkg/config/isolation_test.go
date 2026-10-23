package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStateIsolationProvenanceBindsArtifactAndTrustedRevision(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "plugin")
	require.NoError(t, os.WriteFile(binary, []byte("trusted build artifact"), 0700))
	revision := strings.Repeat("a", 40)
	provenance, err := BuildStateIsolationProvenance(binary, revision, "")
	require.NoError(t, err)
	require.Equal(t, StateIsolationCapability, provenance.Capability)
	require.NoError(t, provenance.VerifyArtifact(binary, revision, ""))
	require.Error(t, provenance.VerifyArtifact(binary, strings.Repeat("b", 40), ""))
	require.NoError(t, os.WriteFile(binary, []byte("replaced artifact"), 0700))
	require.Error(t, provenance.VerifyArtifact(binary, revision, ""))
}

func TestMissingOrInvalidIsolationProvenanceIsUnsupported(t *testing.T) {
	var provenance *StateIsolationProvenance
	require.Error(t, provenance.VerifyArtifact("unused", strings.Repeat("a", 40), ""))
	_, err := BuildStateIsolationProvenance("unused", "unresolved", "")
	require.Error(t, err)
	meta := PluginMeta{}
	require.NoError(t, meta.Load(strings.NewReader("id: legacy\nversion: 1.0\n")))
	require.Nil(t, meta.StateIsolation)
	require.Error(t, meta.StateIsolation.VerifyArtifact("unused", strings.Repeat("a", 40), ""))
}

func TestIsolationMetadataRoundTripsYAML(t *testing.T) {
	meta := PluginMeta{}
	require.NoError(t, meta.Load(strings.NewReader("id: fixed\nstate_isolation:\n  schema_version: 1\n  capability: state-root-v1\n  sdk_module: github.com/omniviewdev/plugin-sdk\n  sdk_revision: "+strings.Repeat("a", 40)+"\n  binary_sha256: "+strings.Repeat("b", 64)+"\n")))
	require.NotNil(t, meta.StateIsolation)
	require.Equal(t, StateIsolationCapability, meta.StateIsolation.Capability)
	require.Equal(t, strings.Repeat("a", 40), meta.StateIsolation.SDKRevision)
}

func TestStateIsolationProvenanceBindsTrustedSourceSnapshot(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "plugin")
	require.NoError(t, os.WriteFile(binary, []byte("trusted dirty-source build"), 0700))
	revision := strings.Repeat("a", 40)
	sourceDigest := strings.Repeat("b", 64)
	provenance, err := BuildStateIsolationProvenance(binary, revision, sourceDigest)
	require.NoError(t, err)
	require.Equal(t, sourceDigest, provenance.SDKSourceSHA256)
	require.NoError(t, provenance.VerifyArtifact(binary, revision, sourceDigest))
	require.Error(t, provenance.VerifyArtifact(binary, revision, strings.Repeat("c", 64)))
	require.Error(t, provenance.VerifyArtifact(binary, revision, ""), "source attestation cannot downgrade to commit-only verification")
	withoutSource := *provenance
	withoutSource.SDKSourceSHA256 = ""
	require.Error(t, withoutSource.VerifyArtifact(binary, revision, sourceDigest), "missing source evidence cannot satisfy a trusted snapshot")
	require.NoError(t, os.WriteFile(binary, []byte("replaced artifact"), 0700))
	require.Error(t, provenance.VerifyArtifact(binary, revision, sourceDigest))
}

func TestStateIsolationRejectsMalformedSourceSnapshots(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "plugin")
	require.NoError(t, os.WriteFile(binary, []byte("trusted build"), 0700))
	revision := strings.Repeat("a", 40)
	sourceDigest := strings.Repeat("b", 64)
	provenance, err := BuildStateIsolationProvenance(binary, revision, sourceDigest)
	require.NoError(t, err)
	for _, malformed := range []string{"snapshot", strings.Repeat("a", 63), strings.Repeat("g", 64), strings.Repeat("A", 64)} {
		_, err := BuildStateIsolationProvenance(binary, revision, malformed)
		require.Error(t, err)
		require.Error(t, provenance.VerifyArtifact(binary, revision, malformed))
		invalidRecord := *provenance
		invalidRecord.SDKSourceSHA256 = malformed
		require.Error(t, invalidRecord.VerifyArtifact(binary, revision, malformed))
	}
}

func TestIsolationMetadataIncludesOptionalSourceSnapshot(t *testing.T) {
	meta := PluginMeta{}
	digest := strings.Repeat("b", 64)
	require.NoError(t, meta.Load(strings.NewReader("id: edited\nstate_isolation:\n  schema_version: 1\n  capability: state-root-v1\n  sdk_module: github.com/omniviewdev/plugin-sdk\n  sdk_revision: "+strings.Repeat("a", 40)+"\n  sdk_source_sha256: "+digest+"\n  binary_sha256: "+strings.Repeat("c", 64)+"\n")))
	require.NotNil(t, meta.StateIsolation)
	require.Equal(t, digest, meta.StateIsolation.SDKSourceSHA256)
	encoded, err := json.Marshal(meta.StateIsolation)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Equal(t, digest, fields["sdk_source_sha256"])
	meta.StateIsolation.SDKSourceSHA256 = ""
	encoded, err = json.Marshal(meta.StateIsolation)
	require.NoError(t, err)
	fields = nil
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.NotContains(t, fields, "sdk_source_sha256")
}
