package faux

import (
	"encoding/hex"
	"errors"
	"testing"
)

// Bitcoin address vectors come from tests/bitcoin_address_reference.py, BIP-173, and BIP-350.
func TestBitcoinScript(t *testing.T) {
	check := func(t *testing.T, address, want string) {
		t.Helper()
		script, err := BitcoinScript(address)
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(script); got != want {
			t.Fatalf("BitcoinScript() = %s; want %s", got, want)
		}
	}
	t.Run("p2wpkh_bip173", func(t *testing.T) {
		check(t, "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "0014751e76e8199196d454941c45d1b3a323f1433bd6")
	})
	t.Run("p2wsh", func(t *testing.T) {
		check(t, "bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3", "00201863143c14c5166804bd19203356da136c985678cd4d27a1b8c6329604903262")
	})
	t.Run("p2tr_bip350", func(t *testing.T) {
		check(t, "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", "512079be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	})
	t.Run("p2pkh", func(t *testing.T) {
		check(t, "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2", "76a91477bff20c60e522dfaa3350c39b030a5d004e839a88ac")
	})
	t.Run("p2sh", func(t *testing.T) {
		check(t, "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy", "a914b472a266d0bd89c13706a4132ccfb16f7c3b9fcb87")
	})
	t.Run("rejects_uppercase_witness", func(t *testing.T) {
		if _, err := BitcoinScript("BC1QW508D6QEJXTDG4Y5R3ZARVARY0C5XW7KV8F3T4"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects_testnet", func(t *testing.T) {
		if _, err := BitcoinScript("tb1qw508d4ejxtdg4y5r3zarvary0c5xw7kxpjzsx"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects_bad_checksum", func(t *testing.T) {
		if _, err := BitcoinScript("bc1qw508d4ejxtdg4y5r3zarvary0c5xw7kv8f3t4"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects_unknown_version", func(t *testing.T) {
		if _, err := BitcoinScript("bc1z0xlxvlhemja6c4dqv22uapctqu8zl3m0"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects_v1_bech32_checksum", func(t *testing.T) {
		if _, err := BitcoinScript("bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqh2y7hd"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects_v0_bech32m_checksum", func(t *testing.T) {
		if _, err := BitcoinScript("bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kemeawh"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects_wrong_program_size", func(t *testing.T) {
		if _, err := BitcoinScript("bc1q0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e7zxg7q"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects_bad_base58_checksum", func(t *testing.T) {
		if _, err := BitcoinScript("1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN3"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects_testnet_base58", func(t *testing.T) {
		if _, err := BitcoinScript("mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
}

func TestEncodeAndDevAddress(t *testing.T) {
	t.Run("bip173_vector", func(t *testing.T) {
		program, _ := hex.DecodeString("751e76e8199196d454941c45d1b3a323f1433bd6")
		got, err := EncodeBitcoinWitnessV0(program)
		if err != nil || got != "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4" {
			t.Fatalf("EncodeBitcoinWitnessV0() = %q, %v", got, err)
		}
	})
	t.Run("rejects_25_byte_program", func(t *testing.T) {
		if _, err := EncodeBitcoinWitnessV0(make([]byte, 25)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v; want ErrInvalid", err)
		}
	})
	t.Run("btc_labels", func(t *testing.T) {
		if got := DevAddress(BitcoinMainnet, "faucet"); got != "bc1qrhws9xkmvmaftdh2nx8yjmt2480pyd40585hfr" {
			t.Fatalf("faucet = %q", got)
		}
		if got := DevAddress(BitcoinMainnet, "alice"); got != "bc1q77jjd4jk3epnt6d39pk0tj0l056s5zsf33atry" {
			t.Fatalf("alice = %q", got)
		}
		if err := ValidateRecipient(BitcoinMainnet, "bc1qrhws9xkmvmaftdh2nx8yjmt2480pyd40585hfr"); err != nil {
			t.Fatalf("faucet recipient validation: %v", err)
		}
		if err := ValidateRecipient(BitcoinMainnet, "bc1q77jjd4jk3epnt6d39pk0tj0l056s5zsf33atry"); err != nil {
			t.Fatalf("alice recipient validation: %v", err)
		}
	})
	t.Run("eth_labels", func(t *testing.T) {
		if got := DevAddress(EthereumMainnet, "faucet"); got != "0x1b22a97fd46b159920aae5e57d8f9b81e50b60f6" {
			t.Fatalf("faucet = %q", got)
		}
		if got := DevAddress(EthereumMainnet, "alice"); got != "0x71db08fc23fc139f388b9825f1e2d4a211872b22" {
			t.Fatalf("alice = %q", got)
		}
		if err := ValidateRecipient(EthereumMainnet, "0x1b22a97fd46b159920aae5e57d8f9b81e50b60f6"); err != nil {
			t.Fatalf("faucet recipient validation: %v", err)
		}
		if err := ValidateRecipient(EthereumMainnet, "0x71db08fc23fc139f388b9825f1e2d4a211872b22"); err != nil {
			t.Fatalf("alice recipient validation: %v", err)
		}
	})
	t.Run("rejects_zero_eth", func(t *testing.T) {
		if !errors.Is(ValidateEthereumAddress("0x0000000000000000000000000000000000000000"), ErrInvalid) {
			t.Fatal("accepted zero ethereum address")
		}
	})
	t.Run("rejects_uppercase_eth", func(t *testing.T) {
		if !errors.Is(ValidateEthereumAddress("0xABCDEF0000000000000000000000000000000000"), ErrInvalid) {
			t.Fatal("accepted uppercase ethereum address")
		}
	})
	t.Run("rejects_short_eth", func(t *testing.T) {
		if !errors.Is(ValidateEthereumAddress("0x111111111111111111111111111111111111111"), ErrInvalid) {
			t.Fatal("accepted 39-digit ethereum address")
		}
	})
}
