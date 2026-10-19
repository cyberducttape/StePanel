package stepanel

import (
	"errors"
	"io"
	"os"
)

var errStateFileTooLarge = errors.New("state file exceeds size limit")

// readStateFile caps memory use for JSON-backed local state. LimitReader keeps
// the bound effective even if the file grows after it is opened.
func readStateFile(path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 || maxBytes == int64(^uint64(0)>>1) {
		return nil, errors.New("invalid state file size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errStateFileTooLarge
	}
	return data, nil
}
