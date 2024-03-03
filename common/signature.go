package common

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"

	"github.com/dnr/styx/pb"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"google.golang.org/protobuf/proto"
)

func LoadPubKeys(keys []string) ([]signature.PublicKey, error) {
	var out []signature.PublicKey
	for _, pk := range keys {
		if k, err := signature.ParsePublicKey(pk); err != nil {
			return nil, err
		} else {
			out = append(out, k)
		}
	}
	return out, nil
}

func LoadSecretKeys(keyfiles []string) ([]signature.SecretKey, error) {
	var out []signature.SecretKey
	for _, path := range keyfiles {
		if skdata, err := os.ReadFile(path); err != nil {
			return nil, err
		} else if k, err := signature.LoadSecretKey(string(skdata)); err != nil {
			return nil, err
		} else {
			out = append(out, k)
		}
	}
	return out, nil
}

func VerifyMessage(keys []signature.PublicKey, b []byte, msg proto.Message) error {
	if len(keys) == 0 {
		return fmt.Errorf("no public keys provided")
	}

	var sm pb.SignedMessage
	err := proto.Unmarshal(b, &sm)
	if err != nil {
		return fmt.Errorf("error unmarshaling SignedMessage: %w", err)
	}

	sigs := make([]signature.Signature, min(len(sm.KeyId), len(sm.Signature)))
	if len(sigs) == 0 {
		return fmt.Errorf("no signatures in SignedMessage")
	}
	for i := range sigs {
		sigs[i].Name = sm.KeyId[i]
		sigs[i].Data = sm.Signature[i]
	}

	var digest string
	switch sm.HashAlgo {
	case "sha256":
		h := sha256.New()
		h.Write(sm.Msg)
		digest = string(h.Sum(nil))
	default:
		return fmt.Errorf("Unknown SignedMessage hash algo %q", sm.HashAlgo)
	}

	if !signature.VerifyFirst(digest, sigs, keys) {
		return fmt.Errorf("signature verification failed")
	}

	return proto.Unmarshal(sm.Msg, msg)
}

func SignMessage(keys []signature.SecretKey, msg proto.Message) ([]byte, error) {
	b, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("error marshaling msg: %w", err)
	}

	sm := &pb.SignedMessage{
		Msg:       b,
		HashAlgo:  "sha256",
		KeyId:     make([]string, len(keys)),
		Signature: make([][]byte, len(keys)),
	}

	h := sha256.New()
	h.Write(sm.Msg)
	digest := string(h.Sum(nil))

	for i, k := range keys {
		sig, err := k.Sign(rand.Reader, digest)
		if err != nil {
			return nil, err
		}
		sm.KeyId[i] = sig.Name
		sm.Signature[i] = sig.Data
	}

	return proto.Marshal(sm)
}
