package devgate

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A relative link in a document is how one file promises where the next one is.
// The archive is history and keeps whatever it had; everything a reader is
// expected to follow today has to resolve, because a dead link is silent: the
// reader either gives up or, worse, believes it read the document it never
// opened. This shipped once in the Skill, where `references/install.md` linked
// `references/product-manual.md` from inside `references/`.
func TestRelativeDocumentLinksResolve(t *testing.T) {
	root := repoRoot(t)
	pattern := regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	skipDirectories := map[string]bool{
		".git":         true,
		"node_modules": true,
		"archive":      true, // `docs/archive` is retained history, not a live target.
		"adapters":     true, // Copies of `skills/`, compared byte for byte elsewhere.
	}
	checked := 0
	var broken []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		inFence := false
		for index, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			for _, match := range pattern.FindAllStringSubmatch(line, -1) {
				target := match[1]
				if target == "" || strings.HasPrefix(target, "<") || strings.HasPrefix(target, "#") {
					continue
				}
				if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				reference := strings.SplitN(target, "#", 2)[0]
				if reference == "" {
					continue
				}
				checked++
				if _, err := os.Stat(filepath.Join(filepath.Dir(path), reference)); err != nil {
					relative, relativeErr := filepath.Rel(root, path)
					if relativeErr != nil {
						relative = path
					}
					broken = append(broken, relative+":"+strconv.Itoa(index+1)+" -> "+target)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no relative document links were found; the scan is looking in the wrong place")
	}
	if len(broken) > 0 {
		t.Fatalf("%d relative document link(s) do not resolve:\n%s", len(broken), strings.Join(broken, "\n"))
	}
}
