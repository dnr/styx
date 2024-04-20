package common

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"

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

// Embedded message must be inline in entry.
func VerifyInlineMessage(
	keys []signature.PublicKey,
	expectedContext string,
	b []byte,
	msg proto.Message,
) error {
	entry, _, err := VerifyMessageAsEntry(keys, expectedContext, b)
	if err != nil {
		return err
	}
	if entry.Size != int64(len(entry.InlineData)) {
		return fmt.Errorf("SignedMessage missing inline data")
	}
	return proto.Unmarshal(entry.InlineData, msg)
}

func VerifyMessageAsEntry(keys []signature.PublicKey, expectedContext string, b []byte) (*pb.Entry, *pb.GlobalParams, error) {
	if len(keys) == 0 {
		return nil, nil, fmt.Errorf("no public keys provided")
	}

	var sm pb.SignedMessage
	err := proto.Unmarshal(b, &sm)
	if err != nil {
		return nil, nil, fmt.Errorf("error unmarshaling SignedMessage: %w", err)
	} else if sm.Msg == nil {
		return nil, nil, fmt.Errorf("SignedMessage missing entry")
	} else if sm.Msg.Path != expectedContext && !strings.HasPrefix(sm.Msg.Path, expectedContext+"/") {
		return nil, nil, fmt.Errorf("SignedMessage context mismatch: %q != %q", sm.Msg.Path, expectedContext)
	} else if len(sm.Msg.Digests) > 0 && sm.Params == nil {
		return nil, nil, fmt.Errorf("SignedMessage with chunks must have params")
	}

	sigs := make([]signature.Signature, min(len(sm.KeyId), len(sm.Signature)))
	if len(sigs) == 0 {
		return nil, nil, fmt.Errorf("no signatures in SignedMessage")
	}
	for i := range sigs {
		sigs[i].Name = sm.KeyId[i]
		sigs[i].Data = sm.Signature[i]
	}

	fingerprint := entryFingerprint(sm.Msg)
	if !signature.VerifyFirst(fingerprint, sigs, keys) {
		return nil, nil, fmt.Errorf("signature verification failed")
	}

	return sm.Msg, sm.Params, nil
}

func SignInlineMessage(keys []signature.SecretKey, context string, msg proto.Message) ([]byte, error) {
	b, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("error marshaling msg: %w", err)
	}
	return SignMessageAsEntry(keys, nil, &pb.Entry{
		Path:       context,
		Type:       pb.EntryType_REGULAR,
		Size:       int64(len(b)),
		InlineData: b,
	})
}

func SignMessageAsEntry(keys []signature.SecretKey, params *pb.GlobalParams, e *pb.Entry) ([]byte, error) {
	sm := &pb.SignedMessage{
		Msg:       e,
		Params:    params,
		KeyId:     make([]string, len(keys)),
		Signature: make([][]byte, len(keys)),
	}

	fingerprint := entryFingerprint(sm.Msg)
	for i, k := range keys {
		sig, err := k.Sign(rand.Reader, fingerprint)
		if err != nil {
			return nil, err
		}
		sm.KeyId[i] = sig.Name
		sm.Signature[i] = sig.Data
	}

	return proto.Marshal(sm)
}

func entryFingerprint(e *pb.Entry) string {
	// TODO: do we need to include params here?
	var sb strings.Builder
	sb.Grow(40 + len(e.Path) + len(e.InlineData) + len(e.Digests))
	sb.WriteString("styx-signed-message-1")
	sb.WriteByte(0)
	if strings.IndexByte(e.Path, 0) != -1 {
		panic("nil in entry path")
	}
	sb.WriteString(e.Path)
	sb.WriteByte(0)
	sb.WriteString(strconv.Itoa(int(e.Size)))
	if len(e.InlineData) > 0 {
		sb.WriteByte(1)
		sb.Write(e.InlineData)
	} else {
		sb.WriteByte(2)
		sb.Write(e.Digests)
	}
	return sb.String()
}
