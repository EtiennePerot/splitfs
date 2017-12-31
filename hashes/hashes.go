package hashes

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"sort"
)

// Hash is a size-agnostic version of the hash.Hash interface.
type Hash interface {
	// Write implements io.Writer.
	Write([]byte) (int, error)
	// Digest returns the digest of the hash with filename-friendly characters,
	// along with a 64-bit hash used for inode numbers.
	Digest() (string, uint64)
}

type hash64Hex struct{ hash.Hash64 }

func (h hash64Hex) Digest() (string, uint64) {
	sum := h.Sum64()
	return fmt.Sprintf("%016x", sum), sum
}

var (
	base32Encoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)
	base64Encoding = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-+").WithPadding(base64.NoPadding)
)

type hashBase32 struct{ hash.Hash }

func (h hashBase32) Digest() (string, uint64) {
	sum := h.Sum(nil)
	return base32Encoding.EncodeToString(sum), binary.LittleEndian.Uint64(sum)
}

type hashBase64 struct{ hash.Hash }

func (h hashBase64) Digest() (string, uint64) {
	sum := h.Sum(nil)
	return base64Encoding.EncodeToString(sum), binary.LittleEndian.Uint64(sum)
}

type HashFunc func() Hash

var hashFuncs = map[string]HashFunc{
	"fnv64a-hex":  func() Hash { return hash64Hex{fnv.New64a()} },
	"fnv64a-b32":  func() Hash { return hashBase32{fnv.New64a()} },
	"fnv128a-b32": func() Hash { return hashBase32{fnv.New128a()} },
	"sha224-b32":  func() Hash { return hashBase32{sha256.New224()} },
	"sha224-b64":  func() Hash { return hashBase64{sha256.New224()} },
	"sha256-b32":  func() Hash { return hashBase32{sha256.New()} },
	"sha256-b64":  func() Hash { return hashBase64{sha256.New()} },
	"sha384-b32":  func() Hash { return hashBase32{sha512.New384()} },
	"sha384-b64":  func() Hash { return hashBase64{sha512.New384()} },
	"sha512-b32":  func() Hash { return hashBase32{sha512.New()} },
	"sha512-b64":  func() Hash { return hashBase64{sha512.New()} },
}

// List of all hash functions.
var HashNames []string

func GetHashFunc(name string) HashFunc {
	return hashFuncs[name]
}

func init() {
	HashNames = make([]string, 0, len(hashFuncs))
	for hashName := range hashFuncs {
		HashNames = append(HashNames, hashName)
	}
	sort.Strings(HashNames)
}
