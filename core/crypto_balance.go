package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	//lint:ignore SA1019 Bitcoin P2PKH addresses are defined as RIPEMD-160(SHA-256(pubkey)); there is no substitute.
	"golang.org/x/crypto/ripemd160"
	"golang.org/x/crypto/sha3"
)

// Real balance verification for leaked private keys.
//
// The "crypto_balance" verifier used to be a shape check that advertised public
// RPC endpoints and then returned "has balance" without ever issuing a request,
// so a leaked key was labelled funded no matter what. The functions here derive
// the address from the key and ask a public node for its balance.
//
// Network failure is treated as "unverified", never as "empty": a node being
// unreachable must not cause a real key to be dropped from the report.

const (
	// rpcTimeout bounds a single balance lookup.
	rpcTimeout = 8 * time.Second
	// rpcSpacing paces outbound lookups so a directory full of candidate keys
	// does not hammer the public endpoints.
	rpcSpacing = 150 * time.Millisecond
)

// balanceResult caches one key's verification so repeated matches of the same
// key (a repo containing it many times, or several chain-named signatures that
// share the EVM key) do not re-query the network.
type balanceResult struct {
	funded      bool
	description string
}

var (
	balanceCacheMu sync.Mutex
	balanceCache   = make(map[string]balanceResult)

	balanceThrottleMu sync.Mutex
	balanceLastCall   time.Time
)

func cachedBalance(key string) (balanceResult, bool) {
	balanceCacheMu.Lock()
	defer balanceCacheMu.Unlock()
	r, ok := balanceCache[key]
	return r, ok
}

func storeBalance(key string, r balanceResult) {
	balanceCacheMu.Lock()
	balanceCache[key] = r
	balanceCacheMu.Unlock()
}

// paceBalanceLookup blocks until at least rpcSpacing has elapsed since the last
// outbound lookup.
func paceBalanceLookup() {
	balanceThrottleMu.Lock()
	defer balanceThrottleMu.Unlock()
	if wait := rpcSpacing - time.Since(balanceLastCall); wait > 0 {
		time.Sleep(wait)
	}
	balanceLastCall = time.Now()
}

// --- EVM ---------------------------------------------------------------------

// evmAddressFromPrivateKey derives the lowercase 0x-prefixed address for a
// 32-byte secp256k1 key: keccak256 of the uncompressed public key (minus the
// 0x04 prefix), last 20 bytes.
func evmAddressFromPrivateKey(privateKeyHex string) (string, error) {
	keyBytes, err := hex.DecodeString(strings.TrimPrefix(privateKeyHex, "0x"))
	if err != nil {
		return "", fmt.Errorf("not hex: %w", err)
	}
	if len(keyBytes) != 32 {
		return "", fmt.Errorf("private key must be 32 bytes, got %d", len(keyBytes))
	}
	if isZeroBytes(keyBytes) {
		return "", fmt.Errorf("private key is zero")
	}

	priv := secp256k1.PrivKeyFromBytes(keyBytes)
	pub := priv.PubKey().SerializeUncompressed() // 65 bytes: 0x04 || X || Y

	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(pub[1:])
	sum := hasher.Sum(nil)

	return "0x" + hex.EncodeToString(sum[12:]), nil
}

func isZeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// evmChains are the public endpoints queried in order. The first reachable node
// holding a non-zero balance wins.
var evmChains = []struct{ name, rpc string }{
	{"Ethereum", "https://eth.llamarpc.com"},
	{"Polygon", "https://polygon-rpc.com"},
	{"BSC", "https://bsc-dataseed.binance.org"},
	{"Arbitrum", "https://arb1.arbitrum.io/rpc"},
	{"Optimism", "https://mainnet.optimism.io"},
	{"Avalanche", "https://api.avax.network/ext/bc/C/rpc"},
	{"Fantom", "https://rpc.ftm.tools"},
}

type evmRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type evmRPCResponse struct {
	Result string `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func evmBalance(ctx context.Context, rpcURL, address string) (*big.Int, error) {
	payload, err := json.Marshal(evmRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "eth_getBalance",
		Params:  []interface{}{address, "latest"},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpc %s returned %d", rpcURL, resp.StatusCode)
	}

	var out evmRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", out.Error.Code, out.Error.Message)
	}
	if out.Result == "" {
		return nil, fmt.Errorf("rpc %s returned no balance", rpcURL)
	}

	balance, ok := new(big.Int).SetString(strings.TrimPrefix(out.Result, "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("unparseable balance %q", out.Result)
	}
	return balance, nil
}

// checkEthereumBalance derives the EVM address and queries the chains for a
// non-zero balance.
//
// Returns:
//   - (true, "Chain (balance N, address)") when a funded address is found;
//   - (true, "EVM (unverified ...)") when no node could be reached, so the key
//     is kept rather than dropped on a network error;
//   - (false, "EVM (zero balance)") when at least one node answered with zero.
func checkEthereumBalance(privateKeyHex string) (bool, string) {
	cacheKey := "evm:" + strings.ToLower(strings.TrimPrefix(privateKeyHex, "0x"))
	if r, ok := cachedBalance(cacheKey); ok {
		return r.funded, r.description
	}

	result := balanceResult{}
	address, err := evmAddressFromPrivateKey(privateKeyHex)
	if err != nil {
		// Not a usable key: report as empty so the caller can discard it.
		storeBalance(cacheKey, result)
		return false, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()

	reachable := false
	for _, chain := range evmChains {
		paceBalanceLookup()
		balance, err := evmBalance(ctx, chain.rpc, address)
		if err != nil {
			continue
		}
		reachable = true
		if balance.Sign() > 0 {
			result = balanceResult{
				funded:      true,
				description: fmt.Sprintf("%s (balance %s wei, %s)", chain.name, balance.String(), address),
			}
			storeBalance(cacheKey, result)
			return true, result.description
		}
	}

	if reachable {
		result = balanceResult{description: fmt.Sprintf("EVM (zero balance, %s)", address)}
	} else {
		result = balanceResult{funded: true, description: fmt.Sprintf("EVM (unverified, no RPC reachable, %s)", address)}
	}
	storeBalance(cacheKey, result)
	return result.funded, result.description
}

// --- Bitcoin -----------------------------------------------------------------

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Encode(input []byte) string {
	zeroes := 0
	for zeroes < len(input) && input[zeroes] == 0 {
		zeroes++
	}

	num := new(big.Int).SetBytes(input)
	radix := big.NewInt(58)
	mod := new(big.Int)

	var encoded []byte
	for num.Sign() > 0 {
		num.DivMod(num, radix, mod)
		encoded = append(encoded, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeroes; i++ {
		encoded = append(encoded, base58Alphabet[0])
	}
	// reverse
	for i, j := 0, len(encoded)-1; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return string(encoded)
}

func base58Decode(input string) ([]byte, error) {
	num := big.NewInt(0)
	radix := big.NewInt(58)
	for _, ch := range input {
		idx := strings.IndexRune(base58Alphabet, ch)
		if idx < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", ch)
		}
		num.Mul(num, radix)
		num.Add(num, big.NewInt(int64(idx)))
	}

	decoded := num.Bytes()
	// Restore leading zero bytes (each leading '1' is one zero byte).
	zeroes := 0
	for zeroes < len(input) && input[zeroes] == base58Alphabet[0] {
		zeroes++
	}
	return append(make([]byte, zeroes), decoded...), nil
}

func base58CheckEncode(payload []byte) string {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	return base58Encode(append(append([]byte{}, payload...), second[:4]...))
}

func base58CheckDecode(s string) ([]byte, error) {
	raw, err := base58Decode(s)
	if err != nil {
		return nil, err
	}
	if len(raw) < 5 {
		return nil, fmt.Errorf("base58 payload too short")
	}
	payload := raw[:len(raw)-4]
	checksum := raw[len(raw)-4:]

	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	if !bytes.Equal(checksum, second[:4]) {
		return nil, fmt.Errorf("base58 checksum mismatch")
	}
	return payload, nil
}

// btcAddressFromWIF decodes a mainnet WIF key and returns its P2PKH address.
func btcAddressFromWIF(wif string) (string, error) {
	payload, err := base58CheckDecode(wif)
	if err != nil {
		return "", err
	}
	if len(payload) != 33 && len(payload) != 34 {
		return "", fmt.Errorf("unexpected WIF length %d", len(payload))
	}
	if payload[0] != 0x80 {
		return "", fmt.Errorf("not a mainnet WIF key")
	}
	if isZeroBytes(payload[1:33]) {
		return "", fmt.Errorf("private key is zero")
	}

	priv := secp256k1.PrivKeyFromBytes(payload[1:33])
	compressed := len(payload) == 34 && payload[33] == 0x01

	var pub []byte
	if compressed {
		pub = priv.PubKey().SerializeCompressed()
	} else {
		pub = priv.PubKey().SerializeUncompressed()
	}

	sha := sha256.Sum256(pub)
	ripemd := ripemd160.New()
	ripemd.Write(sha[:])
	hash160 := ripemd.Sum(nil)

	return base58CheckEncode(append([]byte{0x00}, hash160...)), nil
}

type mempoolAddressStats struct {
	ChainStats struct {
		FundedTxoSum int64 `json:"funded_txo_sum"`
		SpentTxoSum  int64 `json:"spent_txo_sum"`
	} `json:"chain_stats"`
}

// btcBalance returns the confirmed balance, in satoshis, for an address.
func btcBalance(ctx context.Context, address string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://mempool.space/api/address/"+address, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s v%s", Name, Version))

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("mempool.space returned %d", resp.StatusCode)
	}

	var stats mempoolAddressStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return 0, err
	}
	return stats.ChainStats.FundedTxoSum - stats.ChainStats.SpentTxoSum, nil
}

// checkBitcoinBalance decodes a WIF key, derives its address and queries the
// balance. Same unverified-on-network-error contract as checkEthereumBalance.
func checkBitcoinBalance(wifKey string) (bool, string) {
	cacheKey := "btc:" + wifKey
	if r, ok := cachedBalance(cacheKey); ok {
		return r.funded, r.description
	}

	result := balanceResult{}
	address, err := btcAddressFromWIF(wifKey)
	if err != nil {
		storeBalance(cacheKey, result)
		return false, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	paceBalanceLookup()

	balance, err := btcBalance(ctx, address)
	if err != nil {
		result = balanceResult{funded: true, description: fmt.Sprintf("Bitcoin (unverified, %s)", address)}
	} else if balance > 0 {
		result = balanceResult{funded: true, description: fmt.Sprintf("Bitcoin (balance %d sats, %s)", balance, address)}
	} else {
		result = balanceResult{description: fmt.Sprintf("Bitcoin (zero balance, %s)", address)}
	}

	storeBalance(cacheKey, result)
	return result.funded, result.description
}
