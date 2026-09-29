package v3mfa

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	totpPeriod        = 30 * time.Second
	totpDigits        = 6
	seedBytes         = 20
	recoveryCodeCount = 8
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func associatedData(parts ...string) []byte {
	b, _ := json.Marshal(parts)
	return b
}

func (s *Service) seal(seed []byte, parts ...string) ([]byte, []byte, error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce, err := randomBytes(gcm.NonceSize())
	if err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, seed, associatedData(parts...)), nil
}

func (s *Service) open(keyID string, nonce, ciphertext []byte, parts ...string) ([]byte, error) {
	if keyID != s.keyID || len(nonce) != 12 || len(ciphertext) < 16 {
		return nil, ErrUnavailable
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, ErrUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	seed, err := gcm.Open(nil, nonce, ciphertext, associatedData(parts...))
	if err != nil || len(seed) != seedBytes {
		return nil, ErrUnavailable
	}
	return seed, nil
}

// hotp implements RFC 4226 dynamic truncation. Production uses six digits;
// the digits parameter permits direct checks against the RFC's eight-digit
// TOTP vectors without changing the production wire format.
func hotp(seed []byte, step int64, digits int) string {
	var moving [8]byte
	binary.BigEndian.PutUint64(moving[:], uint64(step))
	mac := hmac.New(sha1.New, seed)
	_, _ = mac.Write(moving[:])
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 0x0f)
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	modulus := uint32(1)
	for range digits {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%modulus)
}

func matchTOTP(seed []byte, code string, now time.Time, lastAccepted int64) (int64, bool) {
	if len(code) != totpDigits || now.Unix() < 0 {
		return 0, false
	}
	for i := range code {
		if code[i] < '0' || code[i] > '9' {
			return 0, false
		}
	}
	current := now.Unix() / int64(totpPeriod/time.Second)
	var acceptedStep int64 = -1
	replayed := false
	for _, step := range []int64{current, current - 1, current + 1, current - 2} {
		if step < 0 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(code), []byte(hotp(seed, step, totpDigits))) == 1 {
			if step <= lastAccepted {
				replayed = true
			} else if step != current-2 && acceptedStep < 0 {
				acceptedStep = step
			}
		}
	}
	// A six-digit value can rarely collide across adjacent steps. The extra
	// lookback only checks replay: a previously accepted step can expire from
	// the ±1 candidate window while an adjacent colliding step remains valid.
	return acceptedStep, acceptedStep >= 0 && !replayed
}

func newRecoveryCodes() ([]string, [][]byte, [][]byte, error) {
	codes := make([]string, 0, recoveryCodeCount)
	salts := make([][]byte, 0, recoveryCodeCount)
	hashes := make([][]byte, 0, recoveryCodeCount)
	for range recoveryCodeCount {
		raw, err := randomBytes(16) // 128 bits, so a fast salted hash is safe.
		if err != nil {
			return nil, nil, nil, err
		}
		salt, err := randomBytes(16)
		if err != nil {
			return nil, nil, nil, err
		}
		canonical := strings.ToUpper(hex.EncodeToString(raw))
		sum := recoveryHash(salt, canonical)
		groups := make([]string, 0, 8)
		for i := 0; i < len(canonical); i += 4 {
			groups = append(groups, canonical[i:i+4])
		}
		codes = append(codes, strings.Join(groups, "-"))
		salts = append(salts, salt)
		hashes = append(hashes, sum)
	}
	return codes, salts, hashes, nil
}

func canonicalRecovery(code string) (string, bool) {
	parts := strings.Split(strings.ToUpper(code), "-")
	if len(parts) != 8 {
		return "", false
	}
	for _, part := range parts {
		if len(part) != 4 {
			return "", false
		}
		for _, ch := range part {
			if !strings.ContainsRune("0123456789ABCDEF", ch) {
				return "", false
			}
		}
	}
	return strings.Join(parts, ""), true
}

func recoveryHash(salt []byte, canonical string) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte("aiops-v3-recovery-v1\x00"))
	_, _ = h.Write(salt)
	_, _ = h.Write([]byte(canonical))
	return h.Sum(nil)
}
