package age

import (
	"fmt"

	"filippo.io/age"
)

type KeyPair struct {
	PublicKey  string
	PrivateKey string
}

// GenerateKeyPair creates a new age X25519 key pair.
func GenerateKeyPair() (KeyPair, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return KeyPair{}, fmt.Errorf("generating age key pair: %w", err)
	}
	return KeyPair{
		PublicKey:  identity.Recipient().String(),
		PrivateKey: identity.String(),
	}, nil
}
