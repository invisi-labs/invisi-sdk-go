package credit

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/invisi-labs/invisi-sdk-go/primitives"
)

func receivingSnapshotFixture(t *testing.T, role Role) (DeliveryState, DeliveryContext, []byte) {
	t.Helper()
	s, f := fixture(t)
	material := f["delivery"].(map[string]any)
	keys := func(name string) primitives.ReceivingKeys {
		v := material[name+"ReceivingKeys"].(map[string]any)
		return primitives.ReceivingKeys{ViewingPublicKey: bytesHex(t, v["viewingPublicKey"].(string)), ClueKey: bytesHex(t, v["clueKey"].(string))}
	}
	d := DeliveryState{State: s, OwnerReceivingKeys: keys("owner"), SpenderReceivingKeys: keys("spender")}
	commitment, err := s.Commitment()
	if err != nil {
		t.Fatal(err)
	}
	name, identity := "owner", s.OwnerWalletIdentity
	if role == Spender {
		name, identity = "spender", s.SpenderWalletIdentity
	}
	return d, DeliveryContext{ProtocolContext: s.ProtocolContext, TokenAddress: s.TokenAddress, Commitment: commitment, RecipientIdentity: identity}, bytesHex(t, material[name+"PrivateKey"].(string))
}

func TestCreditDeliveryRequiresBothValidReceivingSnapshots(t *testing.T) {
	for _, recipient := range []Role{Owner, Spender} {
		for _, snapshot := range []Role{Owner, Spender} {
			for _, bad := range []string{"missing", "short public key", "invalid public key", "short clue key", "invalid clue key"} {
				name := map[Role]string{Owner: "owner", Spender: "spender"}
				t.Run(name[recipient]+"/"+name[snapshot]+"/"+bad, func(t *testing.T) {
					d, c, _ := receivingSnapshotFixture(t, recipient)
					keys := &d.OwnerReceivingKeys
					if snapshot == Spender {
						keys = &d.SpenderReceivingKeys
					}
					switch bad {
					case "missing":
						*keys = primitives.ReceivingKeys{}
					case "short public key":
						keys.ViewingPublicKey = keys.ViewingPublicKey[:32]
					case "invalid public key":
						keys.ViewingPublicKey = make([]byte, 33)
					case "short clue key":
						keys.ClueKey = keys.ClueKey[:31]
					case "invalid clue key":
						keys.ClueKey = make([]byte, 32)
					}
					if _, err := Encrypt(d, c, recipient); err == nil {
						t.Fatal("accepted invalid receiving snapshot")
					}
				})
			}
		}
	}
}

func TestCreditDeliveryRejectsForgedRecipientSnapshotAfterAuthentication(t *testing.T) {
	for _, role := range []Role{Owner, Spender} {
		t.Run(map[Role]string{Owner: "owner", Spender: "spender"}[role], func(t *testing.T) {
			d, c, private := receivingSnapshotFixture(t, role)
			actual, _ := roleKey(d, role)
			ephemeral := make([]byte, 32)
			ephemeral[31] = 99
			key, err := crypto.ToECDSA(ephemeral)
			if err != nil {
				t.Fatal(err)
			}
			otherPublic := crypto.CompressPubkey(&key.PublicKey)
			plain := encodeDelivery(d)
			if _, err = DecodeDeliveryPlaintext(plain, c, role, otherPublic); err == nil || !strings.Contains(err.Error(), "receiving key snapshot mismatch") {
				t.Fatal("accepted wrong recipient public key", err)
			}
			if role == Owner {
				d.OwnerReceivingKeys.ViewingPublicKey = otherPublic
			} else {
				d.SpenderReceivingKeys.ViewingPublicKey = otherPublic
			}
			// Encrypt to the real recipient, with a valid tag, while forging its embedded snapshot.
			header := append([]byte{0x84}, otherPublic...)
			header = append(header, make([]byte, 28)...)
			aead, err := encryption(ephemeral, actual.ViewingPublicKey, actual.ViewingPublicKey, header)
			if err != nil {
				t.Fatal(err)
			}
			ciphertext := aead.Seal(header, header[50:62], encodeDelivery(d), aad(c, header, role))
			if _, err = Decrypt(private, ciphertext, c, role); err == nil || !strings.Contains(err.Error(), "receiving key snapshot mismatch") {
				t.Fatal("forged snapshot did not reach and fail recipient validation", err)
			}
		})
	}
}

func TestCreditDeliveryRejectsRetiredSnapshotFreeEnvelopes(t *testing.T) {
	_, c, private := receivingSnapshotFixture(t, Owner)
	for _, old := range []struct {
		version byte
		size    int
	}{{2, 270}, {3, 400}, {0x84, 270}} {
		envelope := make([]byte, old.size)
		envelope[0] = old.version
		if _, err := Decrypt(private, envelope, c, Owner); err == nil || err.Error() != "unsupported Credit envelope" {
			t.Fatalf("retired format %x/%d bypassed format guard: %v", old.version, old.size, err)
		}
	}
}

func TestCreditDeliveryDecodedReceivingSnapshotsDoNotAliasPlaintext(t *testing.T) {
	d, c, _ := receivingSnapshotFixture(t, Owner)
	d.State.Payload = []byte("application payload")
	plain := encodeDelivery(d)
	decoded, err := DecodeDeliveryPlaintext(plain, c, Owner, d.OwnerReceivingKeys.ViewingPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	clear(plain)
	for i, pair := range [][2][]byte{
		{decoded.OwnerReceivingKeys.ViewingPublicKey, d.OwnerReceivingKeys.ViewingPublicKey},
		{decoded.OwnerReceivingKeys.ClueKey, d.OwnerReceivingKeys.ClueKey},
		{decoded.SpenderReceivingKeys.ViewingPublicKey, d.SpenderReceivingKeys.ViewingPublicKey},
		{decoded.SpenderReceivingKeys.ClueKey, d.SpenderReceivingKeys.ClueKey},
		{decoded.State.Payload, d.State.Payload},
	} {
		if !bytes.Equal(pair[0], pair[1]) {
			t.Fatalf("decoded snapshot field %d aliases plaintext", i)
		}
	}
}
