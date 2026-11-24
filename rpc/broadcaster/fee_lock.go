package broadcaster

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	settlement "github.com/invisi-labs/invisi-sdk-go/protocol/settlement"
)

// FeeLock is a reusable, time-bounded Core tariff, not a submission authorization.
// Increment the model whenever the public gas model changes incompatibly.
const feeLockModel = "core-gas-v1.3.4"
const feeLockType = "FeeLock(address token,address feeRecipient,uint256 numerator,uint256 denominator,uint256 gasPrice,uint256 markupBps,uint256 gasMarginBps,uint256 maxGas,uint256 deadline,string model)"

type FeeLock struct {
	Token        string `json:"token"`
	FeeRecipient string `json:"feeRecipient"`
	Numerator    string `json:"numerator"`
	Denominator  string `json:"denominator"`
	GasPrice     string `json:"gasPrice"`
	MarkupBPS    string `json:"markupBps"`
	GasMarginBPS string `json:"gasMarginBps"`
	MaxGas       string `json:"maxGas"`
	Deadline     string `json:"deadline"`
	Model        string `json:"model"`
}
type SignedFeeLock struct {
	Lock      FeeLock `json:"lock"`
	Signature string  `json:"signature"`
}

func FeeLockDigest(domain settlement.QuoteDomain, lock FeeLock) (common.Hash, error) {
	if domain.ChainID == nil || domain.ChainID.Sign() <= 0 || domain.ChainID.BitLen() > 256 || domain.Protocol == (common.Address{}) || lock.Model != feeLockModel || !common.IsHexAddress(lock.Token) || !common.IsHexAddress(lock.FeeRecipient) || common.HexToAddress(lock.Token) == (common.Address{}) || common.HexToAddress(lock.FeeRecipient) == (common.Address{}) {
		return common.Hash{}, errors.New("invalid fee lock")
	}
	word := func(n *big.Int) []byte { return common.LeftPadBytes(n.Bytes(), 32) }
	hash := func(s string) []byte { return crypto.Keccak256([]byte(s)) }
	domainData := append([]byte{}, hash("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")...)
	domainData = append(domainData, hash("InvisiBroadcaster")...)
	domainData = append(domainData, hash("1")...)
	domainData = append(domainData, word(domain.ChainID)...)
	domainData = append(domainData, common.LeftPadBytes(domain.Protocol.Bytes(), 32)...)
	data := append([]byte{}, hash(feeLockType)...)
	data = append(data, common.LeftPadBytes(common.HexToAddress(lock.Token).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(common.HexToAddress(lock.FeeRecipient).Bytes(), 32)...)
	for i, value := range []string{lock.Numerator, lock.Denominator, lock.GasPrice, lock.MarkupBPS, lock.GasMarginBPS, lock.MaxGas, lock.Deadline} {
		n, err := settlement.Uint256(value)
		if err != nil || n.String() != value || ((i < 3 || i >= 5) && n.Sign() == 0) || ((i == 3 || i == 4) && n.Cmp(big.NewInt(100000)) > 0) || (i >= 5 && !n.IsInt64()) {
			return common.Hash{}, errors.New("invalid fee lock bounds")
		}
		data = append(data, word(n)...)
	}
	data = append(data, hash(lock.Model)...)
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, crypto.Keccak256(domainData), crypto.Keccak256(data)), nil
}

// VerifyFeeLock authenticates the reusable tariff under explicit deployment pins.
func VerifyFeeLock(ticket SignedFeeLock, trust Trust, token, recipient common.Address, now uint64) error {
	digest, err := FeeLockDigest(settlement.QuoteDomain{ChainID: trust.ChainID, Protocol: trust.Protocol}, ticket.Lock)
	if err != nil || common.HexToAddress(ticket.Lock.Token) != token || common.HexToAddress(ticket.Lock.FeeRecipient) != recipient || trust.Signer == (common.Address{}) {
		return errors.New("invalid fee lock binding")
	}
	end, _ := strconv.ParseUint(ticket.Lock.Deadline, 10, 64)
	if end <= now {
		return errors.New("FEE_LOCK_EXPIRED")
	}
	sig, err := hexutil.Decode(ticket.Signature)
	if err != nil || len(sig) != 65 {
		return errors.New("invalid fee lock signature")
	}
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	if !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
		return errors.New("invalid fee lock signature")
	}
	pub, err := crypto.SigToPub(digest[:], sig)
	if err != nil || crypto.PubkeyToAddress(*pub) != trust.Signer {
		return errors.New("untrusted fee lock")
	}
	return nil
}

// ForPreparation returns an isolated pricing client. It never mutates shared clients.
// Missing advertised capability retains the legacy protocol; lock failures never downgrade.
func (c *Client) ForPreparation(ctx context.Context, data []byte, trust Trust) (*Client, error) {
	status, receipt, err := c.preparationStatus(ctx, trust)
	if err != nil {
		return nil, err
	}
	if status.SubmissionHalted {
		return nil, errors.New("broadcaster submission halted")
	}
	if status.FeeLock == nil {
		return c, nil
	}
	cap := status.FeeLock
	if cap.Version != "1" || cap.Model != feeLockModel {
		return nil, errors.New("unsupported fee lock capability")
	}
	if !cap.Core {
		return c, nil
	}
	trust.Signer = c.configuration.QuoteSigner
	request, err := DecodeIntent(data)
	if err != nil {
		return nil, err
	}
	// Only initialized, still-active authority may retain a tariff. Each scoped
	// client receives its own value copy, so it cannot mutate or replace the slot.
	var entry *preparationFeeLock
	var fetch uint64
	if receipt != nil {
		a := c.preparation
		a.mu.Lock()
		entry = a.ticket
		a.mu.Unlock()
		if entry != nil && entry.receipt == receipt && entry.token == request.Token {
			if VerifyFeeLock(entry.ticket, trust, request.Token, c.configuration.FeeRecipient, uint64(time.Now().Unix())) != nil {
				c.evictFeeLock(entry)
				entry = nil
			}
		} else {
			entry = nil
		}
		if entry == nil {
			a.mu.Lock()
			a.fetch++
			fetch = a.fetch
			a.mu.Unlock()
		}
	}
	var ticket SignedFeeLock
	if entry != nil {
		ticket = entry.ticket
	} else {
		if err = c.call(ctx, "broadcaster_lockFee", map[string]any{"data": hexutil.Encode(data)}, &ticket); err != nil {
			return nil, err
		}
	}
	if err = VerifyFeeLock(ticket, trust, request.Token, c.configuration.FeeRecipient, uint64(time.Now().Unix())); err != nil {
		c.evictFeeLock(entry)
		return nil, err
	}
	if receipt != nil {
		if err = c.checkPreparation(ctx, receipt); err != nil {
			return nil, err
		}
		if entry == nil {
			entry = &preparationFeeLock{receipt: receipt, token: request.Token, ticket: ticket}
			a := c.preparation
			a.mu.Lock()
			if a.current == receipt && a.fetch == fetch {
				a.ticket = entry
			}
			a.mu.Unlock()
		}
	}
	trust.ChainID = new(big.Int).Set(trust.ChainID)
	return &Client{preparation: c.preparation, preparationReceipt: receipt, configuration: c.configuration, http: c.http, feeLock: &ticket, feeLockTrust: &trust, feeLockEntry: entry}, nil
}
func (c *Client) addFeeLock(params map[string]any, data []byte) error {
	if receipt, bound := c.preparationSnapshot(); bound {
		if err := c.checkPreparation(context.Background(), receipt); err != nil {
			return err
		}
	}
	if c.feeLock == nil {
		return nil
	}
	request, err := DecodeIntent(data)
	if err != nil {
		return err
	}
	if err = VerifyFeeLock(*c.feeLock, *c.feeLockTrust, request.Token, c.configuration.FeeRecipient, uint64(time.Now().Unix())); err != nil {
		c.evictFeeLock(c.feeLockEntry)
		return err
	}
	params["feeLock"] = *c.feeLock
	return nil
}

// preparationFeeLock is an immutable, in-memory tariff under one exact receipt.
// It deliberately carries no intent, authorization, or persisted operation data.
type preparationFeeLock struct {
	receipt *preparationCapability
	token   common.Address
	ticket  SignedFeeLock
}

func (c *Client) evictFeeLock(entry *preparationFeeLock) {
	if entry == nil || c.preparation == nil {
		return
	}
	a := c.preparation
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ticket == entry {
		a.ticket = nil
		a.fetch++
	}
}

func (c *Client) rejectFeeLock(err error) {
	var rpc *RPCError
	if !errors.As(err, &rpc) {
		return
	}
	if (rpc.Code == -32040 && rpc.Message == "FEE_LOCK_EXPIRED") ||
		(rpc.Code == -32041 && rpc.Message == "FEE_LOCK_INVALID") ||
		(rpc.Code == -32042 && rpc.Message == "FEE_LOCK_GAS_EXCEEDED") {
		c.evictFeeLock(c.feeLockEntry)
	}
}
