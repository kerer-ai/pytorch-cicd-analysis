package report

import (
	"bufio"
	"io"
	"io/fs"
	"os"
	"strings"
)

func LoadOps(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseOps(f)
}

func LoadOpsFS(fsys fs.FS, name string) ([]string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseOps(f)
}

func parseOps(r io.Reader) ([]string, error) {
	var ops []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			ops = append(ops, line)
		}
	}
	return ops, scanner.Err()
}
