package evaldata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func Prepare(ctx context.Context, registry Registry, registrySHA256 string, options Options) (Receipt, error) {
	if err := validateRegistry(registry); err != nil {
		return Receipt{}, err
	}
	if !digestPattern.MatchString(registrySHA256) {
		return Receipt{}, errors.New("registry SHA-256 is invalid")
	}
	if err := validateRoot(options.Root, options.VerifyOnly); err != nil {
		return Receipt{}, err
	}
	selected, err := selectDatasets(registry, options.DatasetIDs)
	if err != nil {
		return Receipt{}, err
	}
	if !options.VerifyOnly {
		for _, directory := range []string{options.Root, filepath.Join(options.Root, "sources"), filepath.Join(options.Root, "raw"), filepath.Join(options.Root, "state")} {
			if err := os.MkdirAll(directory, 0o755); err != nil {
				return Receipt{}, fmt.Errorf("create evaluation directory: %w", err)
			}
		}
	}
	receipt := Receipt{Version: ReceiptVersion, RegistrySHA256: registrySHA256, Datasets: make([]DatasetState, 0, len(selected))}
	for _, dataset := range selected {
		select {
		case <-ctx.Done():
			return Receipt{}, ctx.Err()
		default:
		}
		var state DatasetState
		switch dataset.Source.Kind {
		case "git":
			state, err = prepareGit(ctx, options.Root, dataset, options)
		case "http":
			state, err = prepareHTTP(ctx, options.Root, dataset, options)
		}
		if err != nil {
			return Receipt{}, fmt.Errorf("prepare dataset %s: %w", dataset.ID, err)
		}
		receipt.Datasets = append(receipt.Datasets, state)
	}
	if !options.VerifyOnly {
		if err := writeReceipt(filepath.Join(options.Root, "state", "receipt-v1.json"), receipt); err != nil {
			return Receipt{}, err
		}
	}
	return receipt, nil
}

func validateRoot(root string, verifyOnly bool) error {
	if !absoluteNormalized(root) || root == string(filepath.Separator) {
		return errors.New("evaluation root must be a safe absolute normalized path")
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == root {
		return errors.New("evaluation root must not be the user home directory")
	}
	if cwd, err := os.Getwd(); err == nil && filepath.Clean(cwd) == root {
		return errors.New("evaluation root must not be the repository root")
	}
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) && !verifyOnly {
			return nil
		}
		return fmt.Errorf("inspect evaluation root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("evaluation root must be a real directory")
	}
	return nil
}

func selectDatasets(registry Registry, requested []string) ([]Dataset, error) {
	if len(requested) == 0 {
		return registry.Datasets, nil
	}
	wanted := make(map[string]struct{}, len(requested))
	for _, id := range requested {
		if _, duplicate := wanted[id]; duplicate {
			return nil, fmt.Errorf("dataset %q was selected more than once", id)
		}
		wanted[id] = struct{}{}
	}
	selected := make([]Dataset, 0, len(wanted))
	for _, dataset := range registry.Datasets {
		if _, ok := wanted[dataset.ID]; ok {
			selected = append(selected, dataset)
			delete(wanted, dataset.ID)
		}
	}
	for id := range wanted {
		return nil, fmt.Errorf("dataset %q is not in the registry", id)
	}
	return selected, nil
}

func prepareHTTP(ctx context.Context, root string, dataset Dataset, options Options) (DatasetState, error) {
	state := DatasetState{ID: dataset.ID, Kind: "http"}
	for _, artifact := range dataset.Source.Artifacts {
		target := filepath.Join(root, "raw", dataset.ID, artifact.Path)
		if err := ensureContained(filepath.Join(root, "raw", dataset.ID), target); err != nil {
			return DatasetState{}, err
		}
		if options.VerifyOnly {
			if err := verifyArtifact(target, artifact); err != nil {
				return DatasetState{}, err
			}
		} else {
			if err := fetchArtifact(ctx, target, artifact, options); err != nil {
				return DatasetState{}, err
			}
		}
		state.Files++
		state.Bytes += artifact.Bytes
		reportProgress(options, Progress{DatasetID: dataset.ID, Path: artifact.Path, State: "verified", Bytes: artifact.Bytes})
	}
	return state, nil
}

func fetchArtifact(ctx context.Context, target string, artifact Artifact, options Options) error {
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("published artifact is not a regular file")
		}
		return verifyArtifact(target, artifact)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	partial := target + ".part"
	if info, err := os.Lstat(partial); err == nil {
		if !info.Mode().IsRegular() || info.Size() > artifact.Bytes {
			return errors.New("partial artifact is invalid")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	retries := options.HTTPRetries
	if retries <= 0 {
		retries = 4
	}
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<min(attempt-1, 4)) * 100 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		complete, err := fetchAttempt(ctx, client, partial, artifact, options)
		if err == nil && complete {
			if err := verifyArtifact(partial, artifact); err != nil {
				return err
			}
			if err := os.Rename(partial, target); err != nil {
				return fmt.Errorf("publish artifact: %w", err)
			}
			return nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("artifact remained incomplete")
	}
	return lastErr
}

func fetchAttempt(ctx context.Context, client *http.Client, partial string, artifact Artifact, options Options) (bool, error) {
	offset := int64(0)
	if info, err := os.Stat(partial); err == nil {
		offset = info.Size()
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if offset > artifact.Bytes {
		return false, errors.New("partial artifact exceeds the expected size")
	}
	if offset == artifact.Bytes {
		return true, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return false, err
	}
	if offset > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch response.StatusCode {
	case http.StatusOK:
		flags |= os.O_TRUNC
		offset = 0
	case http.StatusPartialContent:
		if !strings.HasPrefix(response.Header.Get("Content-Range"), "bytes "+strconv.FormatInt(offset, 10)+"-") {
			return false, errors.New("HTTP partial response has an invalid Content-Range")
		}
		flags |= os.O_APPEND
	default:
		return false, fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	file, err := os.OpenFile(partial, flags, 0o644)
	if err != nil {
		return false, err
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, artifact.Bytes-offset+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	reportProgress(options, Progress{Path: artifact.Path, State: "downloading", Bytes: offset + written})
	if copyErr != nil {
		return false, copyErr
	}
	if syncErr != nil {
		return false, syncErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	return offset+written == artifact.Bytes, nil
}

func verifyArtifact(path string, artifact Artifact) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open artifact %s: %w", artifact.Path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("artifact is not a regular file")
	}
	if info.Size() != artifact.Bytes {
		return fmt.Errorf("artifact %s size mismatch: got %d want %d", artifact.Path, info.Size(), artifact.Bytes)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash artifact %s: %w", artifact.Path, err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return fmt.Errorf("artifact %s SHA-256 mismatch", artifact.Path)
	}
	return nil
}

func prepareGit(ctx context.Context, root string, dataset Dataset, options Options) (DatasetState, error) {
	target := filepath.Join(root, "sources", dataset.ID)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return DatasetState{}, errors.New("Git target is not a real directory")
		}
		if err := verifyGit(ctx, target, dataset.Source.Revision); err != nil {
			return DatasetState{}, err
		}
		return DatasetState{ID: dataset.ID, Kind: "git", Revision: dataset.Source.Revision}, nil
	} else if !os.IsNotExist(err) {
		return DatasetState{}, err
	}
	if options.VerifyOnly {
		return DatasetState{}, errors.New("Git target does not exist")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return DatasetState{}, err
	}
	partial := target + ".partial"
	info, err := os.Lstat(partial)
	if os.IsNotExist(err) {
		if err := os.Mkdir(partial, 0o755); err != nil {
			return DatasetState{}, err
		}
		if _, err := git(ctx, partial, "init", "--quiet"); err != nil {
			return DatasetState{}, err
		}
		if _, err := git(ctx, partial, "remote", "add", "origin", dataset.Source.URL); err != nil {
			return DatasetState{}, err
		}
	} else if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return DatasetState{}, errors.New("Git partial target is invalid")
	} else {
		remote, err := git(ctx, partial, "remote", "get-url", "origin")
		if err != nil || strings.TrimSpace(remote) != dataset.Source.URL {
			return DatasetState{}, errors.New("Git partial target has an unexpected origin")
		}
	}
	reportProgress(options, Progress{DatasetID: dataset.ID, State: "fetching"})
	if _, err := git(ctx, partial, "fetch", "--depth=1", "--no-tags", "origin", dataset.Source.Revision); err != nil {
		return DatasetState{}, err
	}
	if _, err := git(ctx, partial, "checkout", "--force", "--detach", "FETCH_HEAD"); err != nil {
		return DatasetState{}, err
	}
	if err := verifyGit(ctx, partial, dataset.Source.Revision); err != nil {
		return DatasetState{}, err
	}
	if err := os.Rename(partial, target); err != nil {
		return DatasetState{}, fmt.Errorf("publish Git source: %w", err)
	}
	reportProgress(options, Progress{DatasetID: dataset.ID, State: "verified"})
	return DatasetState{ID: dataset.ID, Kind: "git", Revision: dataset.Source.Revision}, nil
}

func verifyGit(ctx context.Context, target, revision string) error {
	head, err := git(ctx, target, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != revision {
		return fmt.Errorf("Git revision mismatch: got %s want %s", strings.TrimSpace(head), revision)
	}
	if _, err := git(ctx, target, "diff", "--quiet", "HEAD", "--"); err != nil {
		return errors.New("Git source has modified tracked files")
	}
	if _, err := git(ctx, target, "diff", "--cached", "--quiet", "HEAD", "--"); err != nil {
		return errors.New("Git source has staged changes")
	}
	return nil
}

func git(ctx context.Context, directory string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	encoded, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", arguments[0], err, strings.TrimSpace(string(encoded)))
	}
	return string(encoded), nil
}

func ensureContained(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("artifact path escapes its dataset root")
	}
	return nil
}

func reportProgress(options Options, progress Progress) {
	if options.Progress != nil {
		options.Progress(progress)
	}
}

func writeReceipt(path string, receipt Receipt) error {
	encoded, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish evaluation receipt: %w", err)
	}
	return nil
}
