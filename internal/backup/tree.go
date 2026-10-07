package backup

import (
	"fmt"
	"os"
	"path/filepath"
)

// ValidateTree validates the filesystem objects that a managed-site backup
// can represent and returns the logical size of its regular files. Symlinks
// and special files are rejected deliberately: preserving a symlink target
// would make restore semantics depend on paths outside the site tree, while
// the site's archive/import and lifecycle paths use the same no-follow policy.
func ValidateTree(root string, maxBytes int64) (int64, error) {
	if root == "" {
		return 0, fmt.Errorf("backup tree root is empty")
	}
	if maxBytes <= 0 {
		return 0, fmt.Errorf("backup tree maximum size must be positive")
	}

	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed site tree contains unsupported symlink %q; StePanel-managed sites must not contain symlinks", path)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed site tree contains unsupported special file %q", path)
		}
		if info.Size() < 0 {
			return fmt.Errorf("negative size for %q", path)
		}
		if total > maxBytes-info.Size() {
			return fmt.Errorf("backup exceeds maximum size of %d bytes", maxBytes)
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
