package sync

import "os"

func readFileAbs(p string) ([]byte, error) { return os.ReadFile(p) }
