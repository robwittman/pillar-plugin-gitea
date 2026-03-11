package main

import (
	"crypto/rand"
	"log"
)

// cryptoRandRead fills b with cryptographically secure random bytes.
func cryptoRandRead(b []byte) {
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("crypto/rand read failed: %v", err)
	}
}
