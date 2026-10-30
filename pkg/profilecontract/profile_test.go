package profilecontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadJSONPreservesAbsenceAndRejectsRedirectedMetadata(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(base, "absent", "profile.json")
	var record Record
	if err := ReadJSON(missing, &record); !os.IsNotExist(err) {
		t.Fatalf("missing metadata must remain distinguishable: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("reader created a parent: %v", err)
	}
	for _, dangling := range []bool{false, true} {
		target := filepath.Join(base, "target.json")
		if !dangling {
			if err := os.WriteFile(target, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "redirect.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		err := ReadJSON(link, &record)
		var protocolError *Error
		if err == nil || os.IsNotExist(err) || !errors.As(err, &protocolError) ||
			protocolError.Code != "conflicting_state_root" {
			t.Fatalf("redirected metadata must not select default: %v", err)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadJSONDoesNotExposeInvalidMetadata(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "profile.json")
	// A JSON type error can include the input's field name. Diagnostics must
	// expose neither that arbitrary key nor the underlying decoder details.
	before := []byte(`{"private-customer-token":{"schemaVersion":"secret"}}`)
	if writeErr := os.WriteFile(path, before, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	var value map[string]Record
	err = ReadJSON(path, &value)
	var protocolError *Error
	if !errors.As(err, &protocolError) || protocolError.Code != "invalid_profile_binding" ||
		strings.Contains(err.Error(), "private-customer-token") ||
		strings.Contains(err.Error(), "secret") {
		t.Fatalf("expected a private invalid-metadata diagnostic: %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("reader changed invalid metadata: %v", readErr)
	}
}

func TestOwnershipJournalRetainsV1SerializedIdentity(t *testing.T) {
	// These are the existing host wire keys, not a new envelope or protocol.
	data := []byte(
		`{"schemaVersion":1,"workspaceID":"986f7163-049d-4087-b00e-02b62390e865","root":"old","phase":"rename","renameTo":"new","manifest":{"schemaVersion":1,"workspaceID":"986f7163-049d-4087-b00e-02b62390e865","slug":"new","profile":{"kind":"development"}},"resources":[{"commonDir":"source"}]}`,
	)
	var journal OwnershipJournal[ManifestIdentity]
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.SchemaVersion != 1 || journal.Root != "old" || journal.RenameTo != "new" || journal.Phase != "rename" ||
		journal.Manifest.Slug != "new" ||
		journal.Manifest.Profile.Kind != "development" ||
		len(journal.Resources) != 1 ||
		journal.Resources[0].CommonDir != "source" {
		t.Fatalf("v1 ownership journal was not decoded: %+v", journal)
	}
	encoded, err := json.Marshal(journal)
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatalf("v1 wire representation changed: %s %v", encoded, err)
	}
}

func TestCanonicalPathResolvesExistingPrefixWithoutCreatingMissingSuffix(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	if mkdirErr := os.Mkdir(target, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	alias := filepath.Join(base, "alias")
	if linkErr := os.Symlink(target, alias); linkErr != nil {
		t.Skipf("symlink unavailable: %v", linkErr)
	}
	got, err := CanonicalPath(filepath.Join(alias, "missing", "state"))
	want := filepath.Join(target, "missing", "state")
	if err != nil || got != want {
		t.Fatalf("expected canonical missing suffix %q, got %q: %v", want, got, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "missing")); !os.IsNotExist(err) {
		t.Fatalf("canonicalization created a missing directory: %v", err)
	}
}

func TestCanonicalPathRejectsDanglingAndLoopingSymlinks(t *testing.T) {
	for _, name := range []string{"dangling", "loop"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			link := filepath.Join(base, "link")
			target := filepath.Join(base, "absent")
			if name == "loop" {
				target = link
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if got, err := CanonicalPath(filepath.Join(link, "state")); err == nil {
				t.Fatalf("redirected existing prefix must not become missing path %q", got)
			}
		})
	}
}
