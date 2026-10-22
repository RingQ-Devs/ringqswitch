package sip

import (
	"crypto/rand"
	"encoding/hex"
)

// BranchMagic is the RFC 3261 branch prefix.
const BranchMagic = "z9hG4bK"

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewBranch returns a new RFC 3261 compliant branch id.
func NewBranch() string { return BranchMagic + randHex(10) }

// NewTag returns a new random tag.
func NewTag() string { return randHex(6) }

// NewCallID returns a new random Call-ID.
func NewCallID() string { return randHex(16) }

// NewUUID returns a random RFC 4122 version 4 UUID string.
func NewUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
