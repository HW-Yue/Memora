package pagestoremigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestReplacementPreMarkerFaultsKeepOldAuthority(t *testing.T) {
	phases := []replacementPhase{
		phaseReplacementStagingCreated, phaseReplacementBuilt,
		phaseReplacementSourceVerified, phaseReplacementRenamed,
		phaseReplacementSynced, phaseReplacementLiveOpened,
		phaseReplacementBeforeMarker,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			_, rows, _, inserted := authorityValues(t, ctx, file, authority)
			oldGeneration := authority.Generation()
			oldMarker := authority.marker
			injected := errors.New("injected replacement fault")
			operations := defaultReplacementOperations()
			operations.checkpoint = func(current replacementPhase) error {
				if current == phase {
					return injected
				}
				return nil
			}
			if _, err := authority.replaceGeneration(ctx, operations); !errors.Is(err, injected) || errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("replace at %s error = %v", phase, err)
			}
			if authority.Generation() != oldGeneration || authority.marker != oldMarker {
				t.Fatalf("pre-marker fault %s changed in-memory authority", phase)
			}
			marker, err := decodeAuthorityMarker(directory)
			if err != nil || marker != oldMarker {
				t.Fatalf("pre-marker fault %s changed durable marker = %+v, %v", phase, marker, err)
			}
			if got, err := rows.Get(ctx, "work", "notes", inserted.ID); err != nil || got.ID != inserted.ID {
				t.Fatalf("old authority after %s = %+v, %v", phase, got, err)
			}
			assertNoReplacementStaging(t, directory)
		})
	}
}

func TestReplacementBuildTreeFaultsKeepOldAuthority(t *testing.T) {
	for _, phase := range []applyPhase{
		phaseCatalogBuilt, phaseCurrentBuilt, phaseVersionsBuilt, phaseManifestSynced,
	} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			_, rows, _, inserted := authorityValues(t, ctx, file, authority)
			oldGeneration := authority.Generation()
			injected := errors.New("injected Tree build fault")
			operations := defaultReplacementOperations()
			operations.buildCheckpoint = func(current applyPhase) error {
				if current == phase {
					return injected
				}
				return nil
			}
			if _, err := authority.replaceGeneration(ctx, operations); !errors.Is(err, injected) {
				t.Fatalf("replace at %s error = %v", phase, err)
			}
			if authority.Generation() != oldGeneration {
				t.Fatalf("Tree build fault %s changed generation", phase)
			}
			if got, err := rows.Get(ctx, "work", "notes", inserted.ID); err != nil || got.ID != inserted.ID {
				t.Fatalf("old authority after %s = %+v, %v", phase, got, err)
			}
			assertNoReplacementStaging(t, directory)
		})
	}
}

func TestReplacementDurabilityAndOpenFaultsKeepOldAuthority(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*replacementOperations)
	}{
		{
			name: "create staging",
			configure: func(operations *replacementOperations) {
				operations.mkdirTemp = func(string, string) (string, error) {
					return "", errors.New("mkdir fault")
				}
			},
		},
		{
			name: "generation rename",
			configure: func(operations *replacementOperations) {
				operations.rename = func(string, string) error { return errors.New("rename fault") }
			},
		},
		{
			name: "generation parent fsync",
			configure: func(operations *replacementOperations) {
				operations.syncDirectory = func(string) error { return errors.New("fsync fault") }
			},
		},
		{
			name: "strict open",
			configure: func(operations *replacementOperations) {
				operations.strictOpen = func(string) (*Generation, error) {
					return nil, errors.New("strict open fault")
				}
			},
		},
		{
			name: "live open",
			configure: func(operations *replacementOperations) {
				operations.liveOpen = func(string) (*Generation, error) {
					return nil, errors.New("live open fault")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			_, rows, _, inserted := authorityValues(t, ctx, file, authority)
			oldGeneration := authority.Generation()
			oldMarker := authority.marker
			operations := defaultReplacementOperations()
			test.configure(&operations)
			if _, err := authority.replaceGeneration(ctx, operations); err == nil || errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("replace error = %v", err)
			}
			if authority.Generation() != oldGeneration || authority.marker != oldMarker {
				t.Fatal("pre-marker I/O fault changed authority")
			}
			if got, err := rows.Get(ctx, "work", "notes", inserted.ID); err != nil || got.ID != inserted.ID {
				t.Fatalf("old authority after I/O fault = %+v, %v", got, err)
			}
			assertNoReplacementStaging(t, directory)
		})
	}
}

func TestReplacementOrphanRetryStrictlyReusesAndSwaps(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	authorityValues(t, ctx, file, authority)
	operations := defaultReplacementOperations()
	injected := errors.New("crash after generation rename")
	operations.checkpoint = func(phase replacementPhase) error {
		if phase == phaseReplacementRenamed {
			return injected
		}
		return nil
	}
	if _, err := authority.replaceGeneration(ctx, operations); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	receipt, err := authority.ReplaceGeneration(ctx)
	if err != nil || !receipt.Reused || receipt.Epoch != 1 {
		t.Fatalf("replacement retry = %+v, %v", receipt, err)
	}
}

func TestReplacementMarkerOutcomeUnknownPoisonsAndReopenChoosesMarker(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*replacementOperations)
	}{
		{
			name: "marker parent fsync",
			configure: func(operations *replacementOperations) {
				operations.marker.syncDir = func(string) error { return errors.New("marker fsync fault") }
			},
		},
		{
			name: "after marker publication",
			configure: func(operations *replacementOperations) {
				operations.checkpoint = func(phase replacementPhase) error {
					if phase == phaseReplacementMarkerPublished {
						return errors.New("crash after marker")
					}
					return nil
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			authorityValues(t, ctx, file, authority)
			operations := defaultReplacementOperations()
			test.configure(&operations)
			if _, err := authority.replaceGeneration(ctx, operations); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("outcome-unknown replacement error = %v", err)
			}
			if _, err := authority.Capture(ctx); !errors.Is(err, ErrAuthorityPoisoned) {
				t.Fatalf("poisoned Capture() error = %v", err)
			}
			marker, err := decodeAuthorityMarker(directory)
			if err != nil || marker.Epoch != 1 {
				t.Fatalf("published marker = %+v, %v", marker, err)
			}
			if err := authority.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
			if err != nil {
				t.Fatal(err)
			}
			defer reopenedFile.Close()
			reopened, err := OpenAuthority(ctx, reopenedFile, directory)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.marker.Epoch != 1 || reopened.marker.Generation != marker.Generation {
				t.Fatalf("reopen chose marker %+v, want %+v", reopened.marker, marker)
			}
		})
	}
}

func TestReplacementMarkerRenameFailureKeepsOldAuthorityHealthy(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, _, inserted := authorityValues(t, ctx, file, authority)
	old := authority.marker
	operations := defaultReplacementOperations()
	operations.marker.rename = func(string, string) error { return errors.New("marker rename fault") }
	if _, err := authority.replaceGeneration(ctx, operations); err == nil || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("marker rename replacement error = %v", err)
	}
	if marker, err := decodeAuthorityMarker(directory); err != nil || marker != old {
		t.Fatalf("marker rename failure changed marker = %+v, %v", marker, err)
	}
	if got, err := rows.Get(ctx, "work", "notes", inserted.ID); err != nil || got.ID != inserted.ID {
		t.Fatalf("old authority after marker rename fault = %+v, %v", got, err)
	}
}

func TestReplacementSourceChangeBeforePublishKeepsOldAuthority(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, _, inserted := authorityValues(t, ctx, file, authority)
	operations := defaultReplacementOperations()
	operations.checkpoint = func(phase replacementPhase) error {
		if phase == phaseReplacementBuilt {
			return file.Put(nativestore.ObjectKindConfiguration, 1, "replacement-source-change", []byte("changed"))
		}
		return nil
	}
	if _, err := authority.replaceGeneration(ctx, operations); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("source-changed replacement error = %v", err)
	}
	if got, err := rows.Get(ctx, "work", "notes", inserted.ID); err != nil || got.ID != inserted.ID {
		t.Fatalf("old authority after source change = %+v, %v", got, err)
	}
}

func TestReplacementBuildDoesNotBlockOldReaders(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, _, inserted := authorityValues(t, ctx, file, authority)
	entered := make(chan struct{})
	release := make(chan struct{})
	operations := defaultReplacementOperations()
	var once sync.Once
	operations.checkpoint = func(phase replacementPhase) error {
		if phase == phaseReplacementStagingCreated {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := authority.replaceGeneration(ctx, operations)
		done <- err
	}()
	<-entered
	if got, err := rows.Get(ctx, "work", "notes", inserted.ID); err != nil || got.ID != inserted.ID {
		t.Fatalf("reader during replacement build = %+v, %v", got, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func assertNoReplacementStaging(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".page-replacement-v1.staging-") {
			t.Fatalf("replacement staging remains: %s", entry.Name())
		}
	}
}
