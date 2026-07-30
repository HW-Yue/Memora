package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/nativesnapshot"
	"github.com/HW-Yue/Memora/internal/snapshot"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func main() {
	source := flag.String("source", "", "legacy prototype.sqlite path")
	target := flag.String("target", "", "new database.memora path")
	flag.Parse()
	if *source == "" || *target == "" {
		fail("--source and --target are required")
	}
	if err := migrate(context.Background(), *source, *target); err != nil {
		fail(err.Error())
	}
	fmt.Println("verified native migration published:", *target)
}

func migrate(ctx context.Context, source, target string) error {
	legacy, err := openSQLite(source)
	if err != nil {
		return err
	}
	exported, exportErr := snapshot.New(legacy).Export(ctx)
	closeErr := legacy.Close()
	if exportErr != nil || closeErr != nil {
		return fmt.Errorf("export legacy snapshot: %v %v", exportErr, closeErr)
	}
	want, err := snapshot.CanonicalHash(exported)
	if err != nil {
		return err
	}
	backup := source + ".pre-native.bak"
	if err := copyExclusive(source, backup); err != nil {
		return fmt.Errorf("create legacy backup: %w", err)
	}
	directory := filepath.Dir(target)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".memora-migrate-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	_ = temporary.Close()
	_ = os.Remove(temporaryPath)
	defer func() { _ = os.Remove(temporaryPath) }()
	file, err := nativestore.Create(temporaryPath, nativestore.FileKindDatabase)
	if err != nil {
		return err
	}
	if err := nativesnapshot.NewNative(file).Import(exported); err != nil {
		_ = file.Close()
		return err
	}
	verified, err := nativesnapshot.NewNative(file).Export()
	if err != nil {
		_ = file.Close()
		return err
	}
	got, err := snapshot.CanonicalHash(verified)
	if err != nil || got != want {
		_ = file.Close()
		return fmt.Errorf("canonical snapshot verification failed")
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, target)
}

func copyExclusive(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer output.Close()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "sqlite-migrator:", message)
	os.Exit(1)
}
