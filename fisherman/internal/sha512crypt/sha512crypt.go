// Package sha512crypt implements the SHA-512 crypt(3) scheme ("$6$") from
// Ulrich Drepper's "Unix crypt using SHA-256 and SHA-512" specification
// (https://www.akkadia.org/drepper/SHA-crypt.txt), as used by glibc and
// libxcrypt for /etc/shadow entries.
//
// fisherman needs it to hand chpasswd a pre-hashed password (`chpasswd -e`)
// on targets where chpasswd's own hashing path — PAM — is unavailable, without
// depending on a host openssl/mkpasswd binary.
package sha512crypt

import (
	"crypto/rand"
	"crypto/sha512"
	"fmt"
	"strconv"
	"strings"
)

const (
	prefix       = "$6$"
	roundsPrefix = "rounds="

	// DefaultRounds is the spec's default; no "rounds=" field is emitted for it.
	DefaultRounds = 5000
	minRounds     = 1000
	maxRounds     = 999999999

	// SaltLen is the maximum (and generated) salt length in characters.
	SaltLen = 16

	alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// Hash returns a "$6$<salt>$<hash>" string for password using DefaultRounds
// and a 16-character salt drawn from crypto/rand.
func Hash(password string) (string, error) {
	salt, err := newSalt()
	if err != nil {
		return "", err
	}
	return Crypt(password, prefix+salt)
}

// Crypt hashes key under setting, which must start with "$6$" and may carry
// a "rounds=<N>$" field before the salt (e.g. "$6$rounds=10000$salt"). As in
// glibc, the salt ends at the next '$' and is truncated to 16 characters, and
// the round count is clamped to [1000, 999999999]. Anything after the salt
// (such as an existing hash) is ignored, so Crypt(key, stored) == stored
// verifies a password.
func Crypt(key, setting string) (string, error) {
	if !strings.HasPrefix(setting, prefix) {
		return "", fmt.Errorf("sha512crypt: setting %q lacks %q prefix", setting, prefix)
	}
	salt := setting[len(prefix):]

	rounds := DefaultRounds
	customRounds := false
	if strings.HasPrefix(salt, roundsPrefix) {
		rest := salt[len(roundsPrefix):]
		if end := strings.IndexByte(rest, '$'); end > 0 {
			n, err := strconv.ParseUint(rest[:end], 10, 64)
			if err == nil {
				rounds = int(min(max(n, minRounds), maxRounds))
				customRounds = true
				salt = rest[end+1:]
			}
		}
	}
	if end := strings.IndexByte(salt, '$'); end >= 0 {
		salt = salt[:end]
	}
	if len(salt) > SaltLen {
		salt = salt[:SaltLen]
	}

	sum := digest([]byte(key), []byte(salt), rounds)

	var b strings.Builder
	b.Grow(len(prefix) + len(roundsPrefix) + 10 + len(salt) + 1 + 86)
	b.WriteString(prefix)
	if customRounds {
		b.WriteString(roundsPrefix)
		b.WriteString(strconv.Itoa(rounds))
		b.WriteByte('$')
	}
	b.WriteString(salt)
	b.WriteByte('$')
	encode(&b, sum)
	return b.String(), nil
}

// digest runs steps 1-21 of the specification and returns the final C.
func digest(key, salt []byte, rounds int) [sha512.Size]byte {
	// Digest B: key, salt, key.
	h := sha512.New()
	h.Write(key)
	h.Write(salt)
	h.Write(key)
	b := h.Sum(nil)

	// Digest A: key, salt, B stretched to len(key), then B or key per bit
	// of len(key).
	h.Reset()
	h.Write(key)
	h.Write(salt)
	n := len(key)
	for ; n > sha512.Size; n -= sha512.Size {
		h.Write(b)
	}
	h.Write(b[:n])
	for n = len(key); n > 0; n >>= 1 {
		if n&1 != 0 {
			h.Write(b)
		} else {
			h.Write(key)
		}
	}
	a := h.Sum(nil)

	// Sequence P: hash of key repeated len(key) times, stretched to len(key).
	h.Reset()
	for range len(key) {
		h.Write(key)
	}
	p := stretch(h.Sum(nil), len(key))

	// Sequence S: hash of salt repeated 16+A[0] times, stretched to len(salt).
	h.Reset()
	for range 16 + int(a[0]) {
		h.Write(salt)
	}
	s := stretch(h.Sum(nil), len(salt))

	c := a
	for i := range rounds {
		h.Reset()
		if i&1 != 0 {
			h.Write(p)
		} else {
			h.Write(c)
		}
		if i%3 != 0 {
			h.Write(s)
		}
		if i%7 != 0 {
			h.Write(p)
		}
		if i&1 != 0 {
			h.Write(c)
		} else {
			h.Write(p)
		}
		c = h.Sum(c[:0])
	}

	var out [sha512.Size]byte
	copy(out[:], c)
	return out
}

// stretch repeats d to exactly n bytes.
func stretch(d []byte, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n; i += len(d) {
		copy(out[i:], d)
	}
	return out
}

// encodeOrder is the spec's byte permutation for the 21 4-character groups;
// byte 63 is encoded alone as the final 2-character group.
var encodeOrder = [21][3]byte{
	{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4},
	{47, 5, 26}, {6, 27, 48}, {28, 49, 7}, {50, 8, 29}, {9, 30, 51},
	{31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13}, {56, 14, 35},
	{15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19},
	{62, 20, 41},
}

func encode(b *strings.Builder, sum [sha512.Size]byte) {
	for _, o := range encodeOrder {
		b64From24(b, sum[o[0]], sum[o[1]], sum[o[2]], 4)
	}
	b64From24(b, 0, 0, sum[63], 2)
}

func b64From24(b *strings.Builder, b2, b1, b0 byte, n int) {
	w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
	for range n {
		b.WriteByte(alphabet[w&0x3f])
		w >>= 6
	}
}

// newSalt draws SaltLen characters uniformly from the crypt alphabet. The
// alphabet has 64 symbols, so masking a random byte to 6 bits is unbiased.
func newSalt() (string, error) {
	var buf [SaltLen]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("sha512crypt: reading random salt: %w", err)
	}
	for i, r := range buf {
		buf[i] = alphabet[r&0x3f]
	}
	return string(buf[:]), nil
}
