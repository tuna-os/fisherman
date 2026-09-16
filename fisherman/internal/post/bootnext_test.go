package post_test

// Tests for FindBootNextID (internal/post/bootnext.go was 0%): EFI boot-entry
// discovery by PARTUUID matching against efibootmgr -v verbose output, using
// the shared setupMockExec helper from post_test.go.

import (
	"errors"
	"testing"

	"github.com/tuna-os/fisherman/internal/post"
)

const efiPart = "/dev/sda1"

// TestFindBootNextID_HappyPath verifies the matching of a PARTUUID (returned
// by blkid in uppercase with dashes) against the efibootmgr HD() descriptor
// (lowercase, dashless) — both sides must be normalised before matching.
func TestFindBootNextID_HappyPath(t *testing.T) {
	mock := setupMockExec(t)
	mock.responses["blkid -s PARTUUID -o value /dev/sda1"] = response(
		"9A8E7F6B-5C4D-4E3F-8A9B-0C1D2E3F4A5B\n", nil)
	mock.responses["efibootmgr -v"] = response(
		"BootCurrent: 0001\nTimeout: 5 seconds\nBootOrder: 0001,0002\n"+
			"Boot0001* ubuntu\tHD(1,GPT,9a8e7f6b-5c4d-4e3f-8a9b-0c1d2e3f4a5b,0x800,0x82000)/File(\\EFI\\ubuntu\\shimx64.efi)\n"+
			"Boot0002* UEFI Shell\tHD(1,GPT,aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee,0x800,0x82000)/File(\\EFI\\tools\\shellx64.efi)\n",
		nil)

	id, err := post.FindBootNextID(efiPart)
	if err != nil {
		t.Fatalf("FindBootNextID: %v", err)
	}
	if id != "0001" {
		t.Errorf("got boot id %q, want 0001", id)
	}
}

// TestFindBootNextID_InactiveEntriesSkipped verifies that non-active boot
// entries (no '*' at column 8) are never returned, even when their descriptor
// matches the PARTUUID.
func TestFindBootNextID_InactiveEntriesSkipped(t *testing.T) {
	mock := setupMockExec(t)
	mock.responses["blkid -s PARTUUID -o value /dev/sda1"] = response(
		"9a8e7f6b5c4d4e3f8a9b0c1d2e3f4a5b\n", nil)
	mock.responses["efibootmgr -v"] = response(
		"BootOrder: 0002,0001\n"+
			"Boot0001 ubuntu\tHD(1,GPT,9a8e7f6b5c4d4e3f8a9b0c1d2e3f4a5b,0x800,0x82000)/File(\\EFI\\ubuntu\\shimx64.efi)\n"+
			"Boot0002* Windows\tHD(1,GPT,aaaaaaaa...)\n",
		nil)

	id, err := post.FindBootNextID(efiPart)
	if err != nil {
		t.Fatalf("FindBootNextID: %v", err)
	}
	if id != "" {
		t.Errorf("got boot id %q, want empty (entry is inactive)", id)
	}
}

// TestFindBootNextID_NoMatch verifies graceful degradation (empty id, no
// error) when the PARTUUID appears in no active boot entry.
func TestFindBootNextID_NoMatch(t *testing.T) {
	mock := setupMockExec(t)
	mock.responses["blkid -s PARTUUID -o value /dev/sda1"] = response(
		"11111111-2222-3333-4444-555555555555\n", nil)
	mock.responses["efibootmgr -v"] = response(
		"Boot0001* ubuntu\tHD(1,GPT,aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee,0x800,0x82000)/File(\\EFI\\ubuntu\\shimx64.efi)\n",
		nil)

	id, err := post.FindBootNextID(efiPart)
	if err != nil {
		t.Fatalf("FindBootNextID: %v", err)
	}
	if id != "" {
		t.Errorf("got boot id %q, want empty", id)
	}
}

// TestFindBootNextID_BlkidError propagates a blkid failure.
func TestFindBootNextID_BlkidError(t *testing.T) {
	mock := setupMockExec(t)
	mock.responses["blkid -s PARTUUID -o value /dev/sda1"] = response("", errors.New("blkid: no such device"))

	if _, err := post.FindBootNextID(efiPart); err == nil {
		t.Fatal("expected error from failing blkid, got nil")
	}
}

// TestFindBootNextID_BlkidEmpty verifies the explicit error when blkid finds
// no PARTUUID (partition not yet formatted).
func TestFindBootNextID_BlkidEmpty(t *testing.T) {
	mock := setupMockExec(t)
	mock.responses["blkid -s PARTUUID -o value /dev/sda1"] = response("\n", nil)

	if _, err := post.FindBootNextID(efiPart); err == nil {
		t.Fatal("expected error for empty blkid output, got nil")
	}
}

// TestFindBootNextID_EfibootmgrError propagates an efibootmgr failure.
func TestFindBootNextID_EfibootmgrError(t *testing.T) {
	mock := setupMockExec(t)
	mock.responses["blkid -s PARTUUID -o value /dev/sda1"] = response(
		"9a8e7f6b5c4d4e3f8a9b0c1d2e3f4a5b\n", nil)
	mock.responses["efibootmgr -v"] = response("", errors.New("efibootmgr: operation not permitted"))

	if _, err := post.FindBootNextID(efiPart); err == nil {
		t.Fatal("expected error from failing efibootmgr, got nil")
	}
}

func response(out string, err error) struct {
	out []byte
	err error
} {
	return struct {
		out []byte
		err error
	}{out: []byte(out), err: err}
}
