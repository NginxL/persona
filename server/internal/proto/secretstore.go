package proto

import (
	"crypto/ecdh"
	"crypto/hpke"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// SecretStoreValue is encrypted at the source, opened only by the resident provider node.
type SecretStoreValue struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

func SecretStoreInfo(keyID string) []byte { return []byte("persona secret store v1\x00" + keyID) }
func SealSecretStore(key, keyID string, values []SecretStoreValue) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return nil, errors.New("invalid secret store key")
	}
	pub, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, errors.New("invalid secret store key")
	}
	pk, err := hpke.NewDHKEMPublicKey(pub)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(values)
	if err != nil {
		return nil, errors.New("invalid secret store payload")
	}
	defer clear(plain)
	return hpke.Seal(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), SecretStoreInfo(keyID), plain)
}
