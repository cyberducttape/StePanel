//go:build !linux

package sitefiles

import (
	"errors"
	"os"
)

func renameNoReplace(_ *os.Root, _, _ string) error {
	return errors.New("atomic no-overwrite rename is only supported on Linux")
}
