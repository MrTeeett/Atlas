package userdb

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

// pbkdf2Key derives a key of length keyLen using PBKDF2-HMAC-SHA256.
// Minimal implementation to avoid external deps.
func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	if iter < 1 || keyLen < 1 {
		return nil
	}
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	var out []byte
	var buf [4]byte
	for block := 1; block <= numBlocks; block++ {
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		u := hmacSHA256(password, append(append([]byte{}, salt...), buf[:]...))
		t := make([]byte, len(u))
		copy(t, u)
		for i := 2; i <= iter; i++ {
			u = hmacSHA256(password, u)
			for j := 0; j < len(t); j++ {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return h.Sum(nil)
}
