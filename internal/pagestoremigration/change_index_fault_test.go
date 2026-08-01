package pagestoremigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/changeindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestChangeIndexBuildFaultsCleanStagingBeforeRename(t *testing.T) {
	for _, failed := range []changeIndexPhase{
		phaseChangeStagingCreated, phaseChangeBuilt, phaseChangeBeforeRename,
	} {
		t.Run(string(failed), func(t *testing.T) {
			directory := t.TempDir()
			file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			target := filepath.Join(directory, changeIndexDirectory)
			err = buildAuthorityChangeTreeWithOperations(
				context.Background(), directory, target, file,
				changeIndexOperations{
					mkdirTemp: os.MkdirTemp, rename: os.Rename, removeAll: os.RemoveAll,
					syncDirectory: syncGenerationDirectory,
					checkpoint: func(phase changeIndexPhase) error {
						if phase == failed {
							return errors.New("injected build fault")
						}
						return nil
					},
				},
			)
			if err == nil {
				t.Fatal("faulted build succeeded")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("faulted build published target: %v", err)
			}
			assertNoChangeStaging(t, directory)
		})
	}
}

func TestChangeIndexRenameOutcomeUnknownConvergesOnOpen(t *testing.T) {
	directory := t.TempDir()
	file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	target := filepath.Join(directory, changeIndexDirectory)
	err = buildAuthorityChangeTreeWithOperations(
		context.Background(), directory, target, file,
		changeIndexOperations{
			mkdirTemp: os.MkdirTemp, rename: os.Rename, removeAll: os.RemoveAll,
			syncDirectory: syncGenerationDirectory,
			checkpoint: func(phase changeIndexPhase) error {
				if phase == phaseChangeAfterRename {
					return errors.New("injected post-rename fault")
				}
				return nil
			},
		},
	)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("post-rename build error = %v", err)
	}
	assertNoChangeStaging(t, directory)
	tree, err := openAuthorityChangeTree(context.Background(), directory, file)
	if err != nil {
		t.Fatalf("open after outcome unknown = %v", err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestChangeIndexOpenRejectsUnexpectedEntryAndSymlink(t *testing.T) {
	for _, mutate := range []func(string) error{
		func(directory string) error {
			return os.WriteFile(filepath.Join(directory, "unexpected"), []byte("x"), 0o600)
		},
		func(directory string) error {
			pagePath := filepath.Join(directory, changePageFilename)
			realPath := filepath.Join(directory, "saved.pages")
			if err := os.Rename(pagePath, realPath); err != nil {
				return err
			}
			return os.Symlink(filepath.Base(realPath), pagePath)
		},
	} {
		directory := t.TempDir()
		file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(directory, changeIndexDirectory)
		if err := buildAuthorityChangeTree(context.Background(), directory, target, file); err != nil {
			t.Fatal(err)
		}
		if err := mutate(target); err != nil {
			t.Fatal(err)
		}
		if _, err := openAuthorityChangeTree(context.Background(), directory, file); !errors.Is(err, ErrTargetCorrupt) {
			t.Fatalf("open mutated index error = %v", err)
		}
		_ = file.Close()
	}
}

func TestAuthorityReopenRejectsChangePageCorruption(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Projects"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := authority.ListCommittedChanges(ctx, "", 0, 0, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.changes.runtime.FlushDirty(64); err != nil {
		t.Fatal(err)
	}
	rootID := authority.changes.runtime.State().RootPageID
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := page.Open(filepath.Join(directory, changeIndexDirectory, changePageFilename), changeSpaceID)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.Read(rootID)
	if err != nil {
		t.Fatal(err)
	}
	root.Payload[0] ^= 0xff
	if err := manager.Write(root); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	if _, err := OpenAuthority(ctx, reopenedFile, directory); !errors.Is(err, ErrTargetCorrupt) {
		t.Fatalf("OpenAuthority(corrupt change Page) error = %v", err)
	}
}

func TestAuthorityReopenRejectsImmutableBodyIndexMismatch(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Projects"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := authority.ListCommittedChanges(ctx, "", 0, 0, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.changes.runtime.FlushDirty(64); err != nil {
		t.Fatal(err)
	}
	rootID := authority.changes.runtime.State().RootPageID
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := page.Open(filepath.Join(directory, changeIndexDirectory, changePageFilename), changeSpaceID)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.Read(rootID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := btree.Decode(root)
	if err != nil || node.Kind != btree.KindLeaf {
		t.Fatalf("change root = %+v, %v", node, err)
	}
	replaced := false
	for index := range node.LeafEntries {
		entry := &node.LeafEntries[index]
		if len(entry.Key) != 9 || entry.Key[0] != 1 {
			continue
		}
		var locator changeindex.Locator
		if err := json.Unmarshal(entry.Value, &locator); err != nil {
			t.Fatal(err)
		}
		locator.Checksum = strings.Repeat("f", 64)
		entry.Value, err = json.Marshal(locator)
		if err != nil {
			t.Fatal(err)
		}
		replaced = true
		break
	}
	if !replaced {
		t.Fatal("sequence locator was not found in change root")
	}
	tampered, err := btree.Encode(root.Header, node)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(tampered); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	if _, err := OpenAuthority(ctx, reopenedFile, directory); !errors.Is(err, ErrTargetCorrupt) {
		t.Fatalf("OpenAuthority(body/index mismatch) error = %v", err)
	}
}

func assertNoChangeStaging(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".change-index-v1.staging-") {
			t.Fatalf("change index staging remains: %s", entry.Name())
		}
	}
}
