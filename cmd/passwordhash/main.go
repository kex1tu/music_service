// passwordhash reads a password from stdin and writes its bcrypt hash.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"music-manager/internal/core/auth"
)

func main() {
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 74))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if password == "" || len(password) > 72 {
		fmt.Fprintln(os.Stderr, "password must contain 1–72 bytes")
		os.Exit(1)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
