package tiger

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"slices"
	"strings"
)

// errKey is returned for a private key that cannot be used; it never quotes the key.
var errKey = errors.New("the Tiger private key is not a usable RSA key (PKCS#8 or PKCS#1 PEM, or its base64 body)")

// parseKey reads an RSA private key: PEM (PKCS#8 "PRIVATE KEY" or PKCS#1 "RSA PRIVATE KEY")
// or the bare base64 DER body that Tiger's console shows.
func parseKey(s string) (*rsa.PrivateKey, error) {
	der := []byte(nil)
	if block, _ := pem.Decode([]byte(strings.TrimSpace(s))); block != nil {
		der = block.Bytes
	} else {
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil {
			return nil, errKey
		}
		der = b
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errKey
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errKey
	}
	return rk, nil
}

// signContent is Tiger's string to sign: every parameter as k=v, sorted by name, joined
// with "&" (values are not escaped).
func signContent(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	return b.String()
}

// sign is Tiger's request signature: RSA PKCS#1 v1.5 over SHA-1 of signContent, base64.
// SHA-1 is what Tiger's protocol requires (sign_type "RSA"); it is not our choice.
func sign(key *rsa.PrivateKey, params map[string]string) (string, error) {
	sum := sha1.Sum([]byte(signContent(params)))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA1, sum[:])
	if err != nil {
		return "", errKey
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}
