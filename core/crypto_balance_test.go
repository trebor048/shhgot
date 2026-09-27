package core

import "testing"

// TestEVMAddressFromPrivateKey pins the derivation against the canonical test
// vectors. If this drifts, every balance lookup silently queries the wrong
// address and reports real keys as empty.
func TestEVMAddressFromPrivateKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "private key 1",
			key:  "0000000000000000000000000000000000000000000000000000000000000001",
			want: "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf",
		},
		{
			name: "0x-prefixed private key 1",
			key:  "0x0000000000000000000000000000000000000000000000000000000000000001",
			want: "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf",
		},
		{
			name: "0x46 repeated",
			key:  "4646464646464646464646464646464646464646464646464646464646464646",
			want: "0x9d8a62f656a8d1615c1294fd71e9cfb3e4855a4f",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evmAddressFromPrivateKey(tc.key)
			if err != nil {
				t.Fatalf("evmAddressFromPrivateKey(%q) error: %v", tc.key, err)
			}
			if got != tc.want {
				t.Errorf("evmAddressFromPrivateKey(%q) = %s, want %s", tc.key, got, tc.want)
			}
		})
	}
}

func TestEVMAddressRejectsBadKeys(t *testing.T) {
	for _, key := range []string{
		"",
		"zz",
		"1234",
		"0000000000000000000000000000000000000000000000000000000000000000", // zero
		"00000000000000000000000000000000000000000000000000000000000000zz", // non-hex
	} {
		if _, err := evmAddressFromPrivateKey(key); err == nil {
			t.Errorf("evmAddressFromPrivateKey(%q) = nil error, want error", key)
		}
	}
}

// TestBTCAddressFromWIF pins the P2PKH derivation against the standard Bitcoin
// private-key-1 vectors (compressed and uncompressed).
func TestBTCAddressFromWIF(t *testing.T) {
	cases := []struct {
		name string
		wif  string
		want string
	}{
		{
			name: "uncompressed private key 1",
			wif:  "5HpHagT65TZzG1PH3CSu63k8DbpvD8s5ip4nEB3kEsreAnchuDf",
			want: "1EHNa6Q4Jz2uvNExL497mE43ikXhwF6kZm",
		},
		{
			name: "compressed private key 1",
			wif:  "KwDiBf89QgGbjEhKnhXJuH7LrciVrZi3qYjgd9M7rFU73sVHnoWn",
			want: "1BgGZ9tcN4rm9KBzDn7KprQz87SZ26SAMH",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := btcAddressFromWIF(tc.wif)
			if err != nil {
				t.Fatalf("btcAddressFromWIF(%q) error: %v", tc.wif, err)
			}
			if got != tc.want {
				t.Errorf("btcAddressFromWIF(%q) = %s, want %s", tc.wif, got, tc.want)
			}
		})
	}
}

func TestBTCAddressRejectsBadWIF(t *testing.T) {
	for _, wif := range []string{
		"",
		"notbase58",
		"5HpHagT65TZzG1PH3CSu63k8DbpvD8s5ip4nEB3kEsreAnchuDX", // bad checksum
	} {
		if _, err := btcAddressFromWIF(wif); err == nil {
			t.Errorf("btcAddressFromWIF(%q) = nil error, want error", wif)
		}
	}
}

func TestBase58CheckRoundTrip(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}
	encoded := base58CheckEncode(payload)
	decoded, err := base58CheckDecode(encoded)
	if err != nil {
		t.Fatalf("base58CheckDecode(%q) error: %v", encoded, err)
	}
	if string(decoded) != string(payload) {
		t.Errorf("round trip = %x, want %x", decoded, payload)
	}
}

func TestCryptoKeyFormatChecks(t *testing.T) {
	if !isEVMKeyFormat("4646464646464646464646464646464646464646464646464646464646464646") {
		t.Error("valid EVM key rejected")
	}
	if isEVMKeyFormat("4646") || isEVMKeyFormat("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz") {
		t.Error("invalid EVM key accepted")
	}
	if !isWIFKeyFormat("KwDiBf89QgGbjEhKnhXJuH7LrciVrZi3qYjgd9M7rFU73sVHnoWn") {
		t.Error("valid compressed WIF rejected")
	}
	if isWIFKeyFormat("XwDiBf89QgGbjEhKnhXJuH7LrciVrZi3qYjgd9M7rFU73sVHnoWn") {
		t.Error("WIF with bad prefix accepted")
	}
}

// ValidateCryptoKey on an unrecognised shape must not hit the network and must
// keep the finding.
func TestValidateCryptoKeyUnknownShapeIsKept(t *testing.T) {
	funded, label := ValidateCryptoKey("this-is-not-a-key")
	if !funded {
		t.Fatalf("unknown shape dropped: funded=%v label=%q", funded, label)
	}
	if label != "Crypto (unverified)" {
		t.Errorf("label = %q, want %q", label, "Crypto (unverified)")
	}
}

// TestKnownVerifier guards the config-load validation: a typo must not be
// treated as a real verifier.
func TestKnownVerifier(t *testing.T) {
	for _, name := range []string{
		"openai", "claude", "anthropic", "discord", "telegram", "ssh", "crypto_balance",
	} {
		if !knownVerifier(name) {
			t.Errorf("knownVerifier(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "opneai", "discordd", "balance"} {
		if knownVerifier(name) {
			t.Errorf("knownVerifier(%q) = true, want false", name)
		}
	}
}
