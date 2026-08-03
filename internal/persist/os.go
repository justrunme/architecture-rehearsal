package persist

import "os"

func osReadFile(p string) ([]byte, error) { return os.ReadFile(p) }
