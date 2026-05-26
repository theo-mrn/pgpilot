package age

import (
	"bytes"
	"fmt"

	"filippo.io/age"
	"filippo.io/age/armor"
)

type KeyPair struct {
	PublicKey  string
	PrivateKey string
}

func GenerateKeyPair() (KeyPair, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return KeyPair{}, fmt.Errorf("generating age key: %w", err)
	}

	var buf bytes.Buffer
	w := armor.NewWriter(&buf)
	fmt.Fprintf(w, "%s\n", identity)
	w.Close()

	return KeyPair{
		PublicKey:  identity.Recipient().String(),
		PrivateKey: identity.String(),
	}, nil
}
