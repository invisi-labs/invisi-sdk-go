package primitives

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const receivingVectorFile = "receiving-signature-v1-vectors.json"

func receivingVectors(t *testing.T) []vectorFields {
	t.Helper()
	var fixture struct{ Vectors []vectorFields }
	currentJSON(t, receivingVectorFile, &fixture)
	if len(fixture.Vectors) < 2 {
		t.Fatal("expected multiple shared vectors")
	}
	return fixture.Vectors
}

func TestReceivingSignatureVectors(t *testing.T) {
	for _, vector := range receivingVectors(t) {
		t.Run(vector["ownerAddress"], func(t *testing.T) {
			owner := common.HexToAddress(vector["ownerAddress"])
			assertBytes := func(name string, actual []byte) {
				t.Helper()
				if !bytes.Equal(actual, currentBytes(t, vector[name])) {
					t.Fatalf("%s differs from the shared vector", name)
				}
			}
			for _, input := range []string{vector["ownerAddress"], owner.Hex(), "0x" + strings.ToUpper(vector["ownerAddress"][2:])} {
				message, err := BuildReceivingKeyMessage(input)
				if err != nil || string(message) != vector["message"] {
					t.Fatal("receiving message differs from shared vector", err)
				}
				assertBytes("messageHex", message)
				assertBytes("digest", accounts.TextHash(message))
			}
			for _, encoding := range []string{"signature", "signatureV0", "signatureHighS", "signatureCompact"} {
				t.Run(encoding, func(t *testing.T) {
					signature := currentBytes(t, vector[encoding])
					canonical, err := canonicalReceivingSignature(signature)
					if err != nil {
						t.Fatal(err)
					}
					assertBytes("receivingKeyMaterial", canonical[:64])
					root, err := receivingRootKey(owner, canonical[:64])
					if err != nil {
						t.Fatal(err)
					}
					assertBytes("invisiRootKey", root)
					material, err := DeriveReceivingKeysFromSignature(owner.Hex(), signature)
					if err != nil {
						t.Fatal(err)
					}
					assertBytes(encoding, signature)
					assertBytes("viewingPrivateKey", material.ViewingPrivateKey)
					assertBytes("viewingPublicKey", material.ViewingPublicKey)
					assertBytes("detectionKey", material.DetectionKey)
					assertBytes("clueKey", material.ClueKey)
					if material.OwnerAddress != BigIntToElement(owner.Big()) {
						t.Fatal("receiving derivation changed owner")
					}
					address, err := EncodeLongAddress(material.ReceivingAddress())
					if err != nil || address != vector["receivingAddress"] {
						t.Fatal("long receiving address differs from shared vector", err)
					}
				})
			}
			input := currentBytes(t, vector["privateKey"])
			local, err := DeriveWallet(input)
			if err != nil {
				t.Fatal(err)
			}
			input[31] ^= 1
			assertBytes("privateKey", local.SpendingPrivateKey)
			assertBytes("spendingPublicKey", local.SpendingPublicKey)
			assertBytes("viewingPrivateKey", local.ViewingPrivateKey)
			assertBytes("viewingPublicKey", local.ViewingPublicKey)
			assertBytes("detectionSeed", local.DetectionSeed)
			assertBytes("detectionKey", local.DetectionKey)
			assertBytes("clueKey", local.ClueKey)
			address, err := EncodeLongAddress(ReceivingAddress{local.OwnerAddress, local.ViewingPublicKey, local.ClueKey})
			if err != nil || address != vector["receivingAddress"] {
				t.Fatal("local wallet differs from external receiving keys", err)
			}
		})
	}
}

func TestReceivingSignatureRejectsInvalidOwnerOrSignature(t *testing.T) {
	vectors := receivingVectors(t)
	vector := vectors[0]
	owner := vector["ownerAddress"]
	valid := currentBytes(t, vector["signatureV0"])
	altered := func(change func([]byte)) []byte {
		signature := bytes.Clone(valid)
		change(signature)
		return signature
	}
	key, err := ethcrypto.ToECDSA(currentBytes(t, vector["privateKey"]))
	if err != nil {
		t.Fatal(err)
	}
	wrongMessage := func(message string) []byte {
		t.Helper()
		signature, err := ethcrypto.Sign(accounts.TextHash([]byte(message)), key)
		if err != nil {
			t.Fatal(err)
		}
		return signature
	}
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(vector["message"]), vector["message"])
	for _, input := range []string{"", "0x1234", common.Address{}.Hex(), vector["receivingAddress"], "0x7E5f4552091a69125d5dfcb7b8c2659029395bdf"} {
		if message, err := BuildReceivingKeyMessage(input); err == nil || message != nil {
			t.Fatal("accepted invalid receiving message owner")
		}
		if material, err := DeriveReceivingKeysFromSignature(input, valid); err == nil || material != nil {
			t.Fatal("accepted invalid receiving signer owner")
		}
	}
	for _, candidate := range []struct {
		name, owner string
		signature   []byte
	}{
		{"wrong owner", vectors[1]["ownerAddress"], valid},
		{"63 byte signature", owner, valid[:63]},
		{"66 byte signature", owner, append(bytes.Clone(valid), 0)},
		{"missing signature", owner, nil},
		{"chain encoded parity", owner, altered(func(s []byte) { s[64] = 37 })},
		{"invalid parity", owner, altered(func(s []byte) { s[64] = 2 })},
		{"wrong parity", owner, altered(func(s []byte) { s[64] = 1 })},
		{"zero r", owner, altered(func(s []byte) { clear(s[:32]) })},
		{"zero s", owner, altered(func(s []byte) { clear(s[32:64]) })},
		{"invalid r", owner, altered(func(s []byte) { ethcrypto.S256().Params().N.FillBytes(s[:32]) })},
		{"invalid s", owner, altered(func(s []byte) { ethcrypto.S256().Params().N.FillBytes(s[32:64]) })},
		{"extra LF", owner, wrongMessage(vector["message"] + "\n")},
		{"hex text", owner, wrongMessage(vector["messageHex"])},
		{"repeated personal-sign prefix", owner, wrongMessage(prefixedMessage)},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			if material, err := DeriveReceivingKeysFromSignature(candidate.owner, candidate.signature); err == nil || material != nil {
				t.Fatal("accepted invalid receiving signature")
			}
		})
	}
}

func TestReceivingMaterialDefaultRepresentations(t *testing.T) {
	vector := receivingVectors(t)[0]
	material, err := DeriveReceivingKeysFromSignature(vector["ownerAddress"], currentBytes(t, vector["signature"]))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{fmt.Sprintf("%v %v", material, *material), fmt.Sprintf("%+v %+v", material, *material), fmt.Sprintf("%#v %#v", material, *material), string(encoded)} {
		for _, name := range []string{"signature", "receivingKeyMaterial", "invisiRootKey", "viewingPrivateKey", "detectionSeed", "detectionKey", "spendingPrivateKey"} {
			secret := currentBytes(t, vector[name])
			printedName := strings.ToLower(name)
			lowerText := strings.ToLower(text)
			if strings.Contains(lowerText, printedName+":") || strings.Contains(lowerText, `"`+printedName+`"`) {
				t.Fatalf("default representation leaked the %s field", name)
			}
			if len(secret) > 0 && (strings.Contains(lowerText, hex.EncodeToString(secret)) || strings.Contains(text, base64.StdEncoding.EncodeToString(secret)) || strings.Contains(text, fmt.Sprint(secret))) {
				t.Fatalf("default representation leaked %s", name)
			}
		}
	}
	var public map[string]string
	if err := json.Unmarshal(encoded, &public); err != nil || len(public) != 1 || !strings.EqualFold(public["ownerAddress"], vector["ownerAddress"]) {
		t.Fatal("expected only public owner in default JSON", err)
	}
}

func TestReceivingSignatureWithDifferentValidNonceDerivesDifferentKeys(t *testing.T) {
	vector := receivingVectors(t)[0]
	// This valid test signature uses nonce 1 and the first vector's public test key.
	alternate := currentBytes(t, "0x79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f817982eb3e8045be0112c7dcdecb14940942dc98f0ad3e81fc63dbd1281451863425601")
	material, err := DeriveReceivingKeysFromSignature(vector["ownerAddress"], alternate)
	if err != nil {
		t.Fatal(err)
	}
	address, err := EncodeLongAddress(material.ReceivingAddress())
	if err != nil || address == vector["receivingAddress"] {
		t.Fatal("a different nonce must change the receiving address", err)
	}
}

func TestReceivingLocalSigningReducesRFC6979Digest(t *testing.T) {
	key, err := ethcrypto.ToECDSA(currentBytes(t, receivingVectors(t)[0]["privateKey"]))
	if err != nil {
		t.Fatal(err)
	}
	var digest, reduced [32]byte
	new(big.Int).Add(ethcrypto.S256().Params().N, big.NewInt(1)).FillBytes(digest[:])
	reduced[31] = 1
	want, err := ethcrypto.Sign(reduced[:], key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signReceivingDigest(digest, key)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("RFC6979 must use the digest modulo the group order", err)
	}
	if recovered, err := ethcrypto.SigToPub(digest[:], got); err != nil || !recovered.Equal(&key.PublicKey) {
		t.Fatal("reduced digest signature failed recovery against the original digest", err)
	}
}

func TestSignatureReceivingCanDetectAndDecryptOriginalOwnerNote(t *testing.T) {
	vector := receivingVectors(t)[0]
	material, err := DeriveReceivingKeysFromSignature(vector["ownerAddress"], currentBytes(t, vector["signature"]))
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ResolveAddress(vector["receivingAddress"], nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ProtocolContext{ElementFromUint64(1), ElementFromUint64(2)}
	note := Note{ProtocolContext: ctx, OwnerAddress: recipient.OwnerAddress, TokenAddress: ElementFromUint64(4), Amount: ElementFromUint64(5), Nonce: ElementFromUint64(6), Payload: []byte("memo")}
	identity, err := ComputeNoteIdentity(note.OwnerAddress, note.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ComputeNoteCommitment(note.ChainID, note.ProtocolAddress, identity, note.TokenAddress, note.Amount)
	if err != nil {
		t.Fatal(err)
	}
	clue, err := CreateClue(recipient.ClueKey)
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := DetectClue(material.DetectionKey, clue, 24); err != nil || !matched {
		t.Fatal("receiving key failed to detect the note", err)
	}
	enc := NoteEncryptionContext{ctx, leaf}
	ciphertext, err := EncryptPrivateTransferNote(recipient.ViewingPublicKey, note, enc)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := DecryptPrivateTransferNote(material.ViewingPrivateKey, ciphertext, NoteDecryptionContext{NoteEncryptionContext: enc, TokenAddress: note.TokenAddress, OwnerAddress: material.OwnerAddress})
	if err != nil || !reflect.DeepEqual(opened, note) {
		t.Fatal("receiving key failed to decrypt the note", err)
	}
	local, err := DeriveWallet(currentBytes(t, vector["privateKey"]))
	if err != nil {
		t.Fatal(err)
	}
	request := WalletAuthorizationRequest{Context: ctx, Operation: WalletTransact, Capacity: 1, Words: []fr.Element{ctx.ChainID, ctx.ProtocolAddress, leaf}}
	authorization, err := SignWalletAuthorization(local.SpendingPrivateKey, request)
	if err != nil {
		t.Fatal(err)
	}
	if valid, err := VerifyWalletAuthorization(opened.OwnerAddress, local.SpendingPublicKey, authorization, request); err != nil || !valid {
		t.Fatal("the original account failed to authorize spending", err)
	}
	if valid, err := VerifyWalletAuthorization(opened.OwnerAddress, material.ViewingPublicKey, authorization, request); err != nil || valid {
		t.Fatal("viewing public key accepted as spending authority", err)
	}
}
