package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupForUpdateIncludesUncheckpointedWAL(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "halo.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.db.ExecContext(ctx, "PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetAdmin(ctx, "admin", []byte("hash-one")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetAdmin(ctx, "admin", []byte("hash-two")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatalf("wal size = %v, %v", info, err)
	}
	dest, err := database.BackupForUpdate(ctx)
	if err != nil || dest == "" {
		t.Fatalf("backup = %q, %v", dest, err)
	}
	if filepath.Dir(dest) != filepath.Join(dir, "update-backups") {
		t.Fatalf("backup dir = %q", filepath.Dir(dest))
	}
	dirInfo, err := os.Stat(filepath.Dir(dest))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("backup dir mode = %v, %v", dirInfo, err)
	}
	fileInfo, err := os.Stat(dest)
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, %v", fileInfo, err)
	}
	restored, err := Open(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	admin, err := restored.AdminByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(admin.PasswordHash, []byte("hash-two")) {
		t.Fatalf("restored hash = %q", admin.PasswordHash)
	}
}

func TestBackupForUpdateMemoryIsSkipped(t *testing.T) {
	database, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dest, err := database.BackupForUpdate(context.Background())
	if err != nil || dest != "" {
		t.Fatalf("memory backup = %q, %v", dest, err)
	}
}

func TestBackupForUpdateDurableFileURIIsNotSkipped(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "halo.db")
	database, err := Open(ctx, "file:"+path+"?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.SetAdmin(ctx, "admin", []byte("uri-hash")); err != nil {
		t.Fatal(err)
	}
	dest, err := database.BackupForUpdate(ctx)
	if err != nil || dest == "" {
		t.Fatalf("durable URI backup = %q, %v", dest, err)
	}
	if filepath.Dir(dest) != filepath.Join(filepath.Dir(path), "update-backups") {
		t.Fatalf("backup location = %q", dest)
	}
	restored, err := Open(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	admin, err := restored.AdminByUsername(ctx, "admin")
	if err != nil || !bytes.Equal(admin.PasswordHash, []byte("uri-hash")) {
		t.Fatalf("restored URI admin = %+v, %v", admin, err)
	}
}

func TestBackupForUpdateFailsWhenDirectoryIsBlocked(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := Open(ctx, filepath.Join(dir, "halo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := os.WriteFile(filepath.Join(dir, "update-backups"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.BackupForUpdate(ctx); err == nil {
		t.Fatal("backup succeeded when update-backups was a file")
	}
}
