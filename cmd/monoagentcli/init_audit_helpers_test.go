package main

import "os"

func readSource(path string) ([]byte, error) { return os.ReadFile(path) }
