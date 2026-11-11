package faux

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

const base58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var bech32Chars = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// BitcoinScript decodes a canonical Bitcoin mainnet recipient into its output script.
func BitcoinScript(address string) ([]byte, error) {
	if len(address) == 0 || len(address) > 128 {
		return nil, fmt.Errorf("%w: address length", ErrInvalid)
	}
	for i := 0; i < len(address); i++ {
		if address[i] > 0x7f {
			return nil, fmt.Errorf("%w: non-ASCII address", ErrInvalid)
		}
	}
	if strings.HasPrefix(address, "bc1") || strings.HasPrefix(address, "BC1") {
		return decodeWitness(address)
	}
	decoded, ok := decodeBase58(address)
	if !ok || len(decoded) != 25 {
		return nil, fmt.Errorf("%w: base58 address", ErrInvalid)
	}
	digest := sha256.Sum256(decoded[:21])
	digest = sha256.Sum256(digest[:])
	if !equal(decoded[21:], digest[:4]) {
		return nil, fmt.Errorf("%w: base58 checksum", ErrInvalid)
	}
	script := make([]byte, 0, 25)
	switch decoded[0] {
	case 0:
		script = append(script, 0x76, 0xa9, 0x14)
		script = append(script, decoded[1:21]...)
		script = append(script, 0x88, 0xac)
	case 5:
		script = append(script, 0xa9, 0x14)
		script = append(script, decoded[1:21]...)
		script = append(script, 0x87)
	default:
		return nil, fmt.Errorf("%w: base58 version", ErrInvalid)
	}
	return script, nil
}

// ValidateEthereumAddress accepts the protocol's canonical nonzero mainnet address form.
func ValidateEthereumAddress(address string) error {
	if len(address) != 42 || !strings.HasPrefix(address, "0x") || address[2:] == strings.Repeat("0", 40) {
		return fmt.Errorf("%w: ethereum address", ErrInvalid)
	}
	if _, err := hex.DecodeString(address[2:]); err != nil || strings.ToLower(address) != address {
		return fmt.Errorf("%w: ethereum address", ErrInvalid)
	}
	return nil
}

// ValidateRecipient validates a recipient for one supported mainnet network.
func ValidateRecipient(network, address string) error {
	switch network {
	case BitcoinMainnet:
		_, err := BitcoinScript(address)
		return err
	case EthereumMainnet:
		return ValidateEthereumAddress(address)
	default:
		return fmt.Errorf("%w: network", ErrInvalid)
	}
}

// EncodeBitcoinWitnessV0 encodes a 20- or 32-byte program as a lowercase Bech32 address.
func EncodeBitcoinWitnessV0(program []byte) (string, error) {
	if len(program) != 20 && len(program) != 32 {
		return "", fmt.Errorf("%w: witness program length", ErrInvalid)
	}
	converted, err := convertBits(program, 8, 5, true)
	if err != nil {
		return "", err
	}
	return encodeBech32("bc", append([]byte{0}, converted...), 1), nil
}

// DevAddress derives a deterministic dev recipient for a supported network and label.
func DevAddress(network, label string) string {
	input := []byte("foryou-faux/" + network + "/" + label)
	digest := sha256.Sum256(input)
	switch network {
	case BitcoinMainnet:
		address, _ := EncodeBitcoinWitnessV0(digest[:20])
		return address
	case EthereumMainnet:
		return "0x" + hex.EncodeToString(digest[12:])
	default:
		return ""
	}
}

func decodeBase58(s string) ([]byte, bool) {
	n := new(big.Int)
	base := big.NewInt(58)
	for _, r := range s {
		digit := strings.IndexRune(base58, r)
		if digit < 0 {
			return nil, false
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(digit)))
	}
	decoded := n.Bytes()
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), decoded...), true
}

func decodeWitness(s string) ([]byte, error) {
	if len(s) > 90 || strings.ToLower(s) != s {
		return nil, fmt.Errorf("%w: witness case or length", ErrInvalid)
	}
	separator := strings.LastIndexByte(s, '1')
	if separator < 1 || separator+7 > len(s) || s[:separator] != "bc" {
		return nil, fmt.Errorf("%w: witness address", ErrInvalid)
	}
	values := make([]byte, len(s)-separator-1)
	for i, r := range s[separator+1:] {
		index := strings.IndexRune(bech32Chars, r)
		if index < 0 {
			return nil, fmt.Errorf("%w: witness character", ErrInvalid)
		}
		values[i] = byte(index)
	}
	polymod := bech32Polymod(append(bech32Expand("bc"), values...))
	version := values[0]
	if version == 0 && polymod != 1 || version == 1 && polymod != 0x2bc830a3 || version > 1 {
		return nil, fmt.Errorf("%w: witness checksum or version", ErrInvalid)
	}
	program, err := convertBits(values[1:len(values)-6], 5, 8, false)
	if err != nil || version == 0 && len(program) != 20 && len(program) != 32 || version == 1 && len(program) != 32 {
		return nil, fmt.Errorf("%w: witness program", ErrInvalid)
	}
	if version == 0 {
		return append([]byte{0, byte(len(program))}, program...), nil
	}
	return append([]byte{0x51, byte(len(program))}, program...), nil
}

func convertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	var accumulator uint32
	var bits uint
	maxValue := uint32(1<<to) - 1
	result := make([]byte, 0, (len(data)*int(from)+int(to)-1)/int(to))
	for _, value := range data {
		if uint32(value)>>from != 0 {
			return nil, errors.New("faux: invalid bit group")
		}
		accumulator = accumulator<<from | uint32(value)
		bits += from
		for bits >= to {
			bits -= to
			result = append(result, byte(accumulator>>bits&maxValue))
		}
	}
	if pad {
		if bits > 0 {
			result = append(result, byte(accumulator<<(to-bits)&maxValue))
		}
	} else if bits >= from || accumulator<<(to-bits)&maxValue != 0 {
		return nil, errors.New("faux: noncanonical bit padding")
	}
	return result, nil
}

func encodeBech32(hrp string, data []byte, constant uint32) string {
	values := append(append(bech32Expand(hrp), data...), make([]byte, 6)...)
	polymod := bech32Polymod(values) ^ constant
	var output strings.Builder
	output.WriteString(hrp)
	output.WriteByte('1')
	for _, value := range data {
		output.WriteByte(bech32Chars[value])
	}
	for i := 0; i < 6; i++ {
		shift := uint(5 * (5 - i))
		output.WriteByte(bech32Chars[(polymod>>shift)&31])
	}
	return output.String()
}

func bech32Expand(hrp string) []byte {
	result := make([]byte, 0, len(hrp)*2+1)
	for _, r := range hrp {
		result = append(result, byte(r>>5))
	}
	result = append(result, 0)
	for _, r := range hrp {
		result = append(result, byte(r&31))
	}
	return result
}

func bech32Polymod(values []byte) uint32 {
	generators := [...]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	checksum := uint32(1)
	for _, value := range values {
		top := checksum >> 25
		checksum = (checksum&0x1ffffff)<<5 ^ uint32(value)
		for i, generator := range generators {
			if top>>i&1 != 0 {
				checksum ^= generator
			}
		}
	}
	return checksum
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
