package primitives

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"github.com/invisi-labs/invisi-sdk-go/primitives/internal/cryptography/fmd"
)

// ReceivingKeyMaterial holds the viewing and detection keys for an EVM owner.
// Keep its private keys out of logs and public transport.
type ReceivingKeyMaterial struct {
	OwnerAddress      fr.Element
	ViewingPrivateKey []byte
	ViewingPublicKey  []byte
	DetectionKey      []byte
	ClueKey           []byte
}

// ReceivingAddress returns an independent copy of the public receiving material.
func (material ReceivingKeyMaterial) ReceivingAddress() ReceivingAddress {
	return ReceivingAddress{material.OwnerAddress, bytes.Clone(material.ViewingPublicKey), bytes.Clone(material.ClueKey)}
}

func (material ReceivingKeyMaterial) String() string {
	return fmt.Sprintf("ReceivingKeyMaterial(%s)", common.BigToAddress(ElementToBigInt(material.OwnerAddress)).Hex())
}

func (material ReceivingKeyMaterial) GoString() string { return material.String() }

// MarshalJSON exposes only the public EVM owner address.
func (material ReceivingKeyMaterial) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		OwnerAddress string `json:"ownerAddress"`
	}{common.BigToAddress(ElementToBigInt(material.OwnerAddress)).Hex()})
}

// BuildReceivingKeyMessage returns the fixed UTF-8 message bytes for EIP-191
// personal signing. The owner must be a nonzero 0x address.
func BuildReceivingKeyMessage(owner string) ([]byte, error) {
	address, err := parseReceivingOwner(owner)
	if err != nil {
		return nil, err
	}
	return receivingKeyMessage(address)
}

// DeriveReceivingKeysFromSignature verifies a fixed-message signature from owner
// and derives the viewing and detection keys. The signature may be 65-byte
// r||s||v (v 0/1 or 27/28), or EIP-2098 compact. Treat it as private key material.
func DeriveReceivingKeysFromSignature(owner string, signature []byte) (*ReceivingKeyMaterial, error) {
	address, err := parseReceivingOwner(owner)
	if err != nil {
		return nil, err
	}
	message, err := receivingKeyMessage(address)
	if err != nil {
		return nil, err
	}
	material, seed, err := deriveReceivingKeys(address, accounts.TextHash(message), signature)
	clear(seed)
	return material, err
}

func parseReceivingOwner(owner string) (common.Address, error) {
	if !strings.HasPrefix(owner, "0x") {
		return common.Address{}, fmt.Errorf("expected 0x receiving owner address")
	}
	decoded, err := DecodeAddress(owner)
	if err != nil {
		return common.Address{}, err
	}
	return common.BigToAddress(ElementToBigInt(decoded.OwnerAddress)), nil
}

func receivingKeyMessage(owner common.Address) ([]byte, error) {
	if owner == (common.Address{}) {
		return nil, fmt.Errorf("zero receiving owner address")
	}
	return []byte("Invisi Privacy Receiving Keys v1\nAccount: 0x" + hex.EncodeToString(owner[:]) + "\nPurpose: derive viewing and detection keys only."), nil
}

func signReceivingDigest(digest [32]byte, key *ecdsa.PrivateKey) ([]byte, error) {
	// Some secp256k1 backends pass the hash to RFC6979 unchanged. Apply
	// bits2octets so all backends use the same nonce when the hash is >= n.
	order := ethcrypto.S256().Params().N
	reduced := new(big.Int).SetBytes(digest[:])
	if reduced.Cmp(order) >= 0 {
		reduced.Sub(reduced, order).FillBytes(digest[:])
	}
	return ethcrypto.Sign(digest[:], key)
}

func canonicalReceivingSignature(signature []byte) ([65]byte, error) {
	var canonical [65]byte
	switch len(signature) {
	case 64:
		copy(canonical[:], signature)
		canonical[64] = canonical[32] >> 7
		canonical[32] &= 0x7f
	case 65:
		copy(canonical[:], signature)
		if canonical[64] == 27 || canonical[64] == 28 {
			canonical[64] -= 27
		}
	default:
		return canonical, fmt.Errorf("receiving signature must contain 64 or 65 bytes")
	}
	r := new(big.Int).SetBytes(canonical[:32])
	s := new(big.Int).SetBytes(canonical[32:64])
	defer clear(r.Bits())
	defer clear(s.Bits())
	if !ethcrypto.ValidateSignatureValues(canonical[64], r, s, false) {
		clear(canonical[:])
		return canonical, fmt.Errorf("invalid receiving signature scalars or recovery parity")
	}
	otherS := new(big.Int).Sub(ethcrypto.S256().Params().N, s)
	defer clear(otherS.Bits())
	if s.Cmp(otherS) > 0 {
		otherS.FillBytes(canonical[32:64])
		canonical[64] ^= 1
	}
	return canonical, nil
}

func receivingRootKey(owner common.Address, material []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, material, []byte("Invisi.ReceivingRoot.v1"), string(owner[:]), 32)
}

func deriveReceivingKeys(owner common.Address, digest, signature []byte) (material *ReceivingKeyMaterial, detectionSeed []byte, err error) {
	canonical, err := canonicalReceivingSignature(signature)
	if err != nil {
		return nil, nil, err
	}
	defer clear(canonical[:])
	public, err := ethcrypto.SigToPub(digest, canonical[:])
	if err != nil {
		return nil, nil, fmt.Errorf("invalid receiving signature: %w", err)
	}
	if ethcrypto.PubkeyToAddress(*public) != owner {
		return nil, nil, fmt.Errorf("receiving signature does not match owner")
	}
	root, err := receivingRootKey(owner, canonical[:64])
	if err != nil {
		return nil, nil, err
	}
	defer clear(root)
	view, err := walletScalar(root, "Invisi.WalletViewingKey.v1", false)
	if err != nil {
		return nil, nil, err
	}
	viewPublic, err := ViewingPublicKey(view)
	if err != nil {
		clear(view)
		return nil, nil, err
	}
	seed, err := hkdf.Key(sha256.New, root, []byte("Invisi.WalletDetectionSeed.v1"), "", 32)
	if err != nil {
		clear(view)
		return nil, nil, err
	}
	detection, err := walletScalar(seed, "Invisi.WalletDetectionKey.v1", true)
	if err != nil {
		clear(view)
		clear(seed)
		return nil, nil, err
	}
	clue, err := DeriveClueKey(detection)
	if err != nil {
		clear(view)
		clear(seed)
		clear(detection)
		return nil, nil, err
	}
	return &ReceivingKeyMaterial{
		OwnerAddress:      BigIntToElement(owner.Big()),
		ViewingPrivateKey: view,
		ViewingPublicKey:  viewPublic,
		DetectionKey:      detection,
		ClueKey:           clue,
	}, seed, nil
}

func walletScalar(key []byte, salt string, littleEndian bool) ([]byte, error) {
	for counter := uint64(0); counter <= uint64(^uint32(0)); counter++ {
		var info [4]byte
		binary.BigEndian.PutUint32(info[:], uint32(counter))
		candidate, err := hkdf.Key(sha256.New, key, []byte(salt), string(info[:]), 32)
		if err != nil {
			return nil, err
		}
		if littleEndian {
			candidate[31] &= 7
			if fmd.IsCanonicalDetectionKey(candidate) {
				return candidate, nil
			}
		} else {
			n := new(big.Int).SetBytes(candidate)
			valid := n.Sign() > 0 && n.Cmp(ethcrypto.S256().Params().N) < 0
			clear(n.Bits())
			if valid {
				return candidate, nil
			}
		}
		clear(candidate)
	}
	return nil, fmt.Errorf("wallet scalar derivation exhausted")
}
