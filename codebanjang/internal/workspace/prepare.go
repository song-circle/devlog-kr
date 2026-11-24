package workspace

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CopyIgnored copies files and folders matching the patterns (relative to
// the repository root, * and ? wildcards) from the project folder into the
// work folder. A new worktree has only what git tracks, so files the
// project needs but git ignores (.env, local config) are missing there.
//
// Only paths git ignores are copied: a file git would see would show up in
// the request's diff and end up in its commit. Existing files in the work
// folder are left alone. Returns the copied paths (slash-separated) and the
// patterns that matched nothing.
func CopyIgnored(repo, dst string, patterns []string) (copied, missing []string, err error) {
	for _, pat := range patterns {
		pat = strings.TrimSpace(filepath.FromSlash(pat))
		if pat == "" {
			continue
		}
		if filepath.IsAbs(pat) || strings.HasPrefix(filepath.Clean(pat), "..") {
			return copied, missing, fmt.Errorf("복사할 파일은 프로젝트 폴더 안의 상대 경로여야 합니다: %s", pat)
		}
		matches, err := filepath.Glob(filepath.Join(repo, pat))
		if err != nil {
			return copied, missing, fmt.Errorf("잘못된 패턴 %q: %w", pat, err)
		}
		if len(matches) == 0 {
			missing = append(missing, filepath.ToSlash(pat))
			continue
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(repo, m)
			rel = filepath.ToSlash(rel)
			if rel == ".git" || strings.HasPrefix(rel, ".git/") {
				continue
			}
			if !ignored(repo, rel) {
				return copied, missing, fmt.Errorf("%s 는 git이 무시하지 않는 파일입니다(이미 저장소에 있거나 .gitignore에 없음). "+
					"복사하면 AI의 변경 내용에 섞이므로 .gitignore에 넣거나 목록에서 빼세요", rel)
			}
			if err := copyTree(m, filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
				return copied, missing, fmt.Errorf("%s 복사: %w", rel, err)
			}
			copied = append(copied, rel)
		}
	}
	return copied, missing, nil
}

// ignored reports whether git ignores path (relative, slash-separated).
func ignored(repo, rel string) bool {
	_, err := git(repo, "check-ignore", "-q", "--", rel)
	return err == nil // exit 0 = ignored, 1 = not ignored
}

// copyTree copies a file or a folder; files already at the destination stay.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // links and devices are not copied
		}
		if _, err := os.Lstat(target); err == nil {
			return nil
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
