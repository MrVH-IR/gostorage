package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
)

func New() hash.Hash      { return sha256.New() }
func Sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
