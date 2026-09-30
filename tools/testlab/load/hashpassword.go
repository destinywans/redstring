// Testlab-only password hashing uses the product's actual verifier contract.
// Input/output are pipes to the private seed helper; never log credentials.
package main

import (
	"encoding/json"
	"github.com/wans/aiops-canvas/internal/store"
	"io"
	"os"
	"strings"
)

func main() {
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1025))
	if err != nil || len(input) == 0 || len(input) > 1024 || strings.ContainsAny(string(input), "\r\n") {
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"hash": store.PasswordHash(string(input))})
}
