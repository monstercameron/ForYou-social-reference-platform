package keys

import (
	"bytes"
	"math/big"
)

var (
	fieldPrime       = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	groupOrder       = new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 252), mustBigInt("27742317777372353535851937790883648493"))
	curveD           = fieldMul(big.NewInt(-121665), new(big.Int).ModInverse(big.NewInt(121666), fieldPrime))
	sqrtM1           = new(big.Int).Exp(big.NewInt(2), new(big.Int).Div(new(big.Int).Sub(fieldPrime, big.NewInt(1)), big.NewInt(4)), fieldPrime)
	identityEncoding = append([]byte{1}, make([]byte, 31)...)
)

func mustBigInt(decimal string) *big.Int {
	value, ok := new(big.Int).SetString(decimal, 10)
	if !ok {
		panic("invalid integer constant")
	}
	return value
}

type point struct {
	x, y, z, t *big.Int
}

func fieldAdd(a, b *big.Int) *big.Int { return new(big.Int).Mod(new(big.Int).Add(a, b), fieldPrime) }
func fieldSub(a, b *big.Int) *big.Int { return new(big.Int).Mod(new(big.Int).Sub(a, b), fieldPrime) }
func fieldMul(a, b *big.Int) *big.Int { return new(big.Int).Mod(new(big.Int).Mul(a, b), fieldPrime) }

func identityPoint() point {
	return point{big.NewInt(0), big.NewInt(1), big.NewInt(1), big.NewInt(0)}
}

// This math/big arithmetic is slow and non-constant-time; it is used only on public keys and signatures, never secret scalars.
func decodePoint(encoded []byte) (point, error) {
	if len(encoded) != 32 {
		return point{}, ErrBadKey
	}
	value := littleEndianInt(encoded)
	sign := value.Bit(255)
	y := new(big.Int).And(value, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1)))
	if y.Cmp(fieldPrime) >= 0 {
		return point{}, ErrBadKey
	}
	ySquared := fieldMul(y, y)
	numerator := fieldSub(ySquared, big.NewInt(1))
	denominator := fieldAdd(fieldMul(curveD, ySquared), big.NewInt(1))
	denominatorInverse := new(big.Int).ModInverse(denominator, fieldPrime)
	if denominatorInverse == nil {
		return point{}, ErrBadKey
	}
	xSquared := fieldMul(numerator, denominatorInverse)
	x := new(big.Int).Exp(xSquared, new(big.Int).Div(new(big.Int).Add(fieldPrime, big.NewInt(3)), big.NewInt(8)), fieldPrime)
	if fieldMul(x, x).Cmp(xSquared) != 0 {
		x = fieldMul(x, sqrtM1)
	}
	if fieldMul(x, x).Cmp(xSquared) != 0 || (x.Sign() == 0 && sign == 1) {
		return point{}, ErrBadKey
	}
	if x.Bit(0) != sign {
		x = new(big.Int).Sub(fieldPrime, x)
	}
	return point{x, y, big.NewInt(1), fieldMul(x, y)}, nil
}

func addPoints(a, b point) point {
	aa := fieldMul(fieldSub(a.y, a.x), fieldSub(b.y, b.x))
	bb := fieldMul(fieldAdd(a.y, a.x), fieldAdd(b.y, b.x))
	cc := fieldMul(fieldMul(big.NewInt(2), curveD), fieldMul(a.t, b.t))
	dd := fieldMul(big.NewInt(2), fieldMul(a.z, b.z))
	e := fieldSub(bb, aa)
	f := fieldSub(dd, cc)
	g := fieldAdd(dd, cc)
	h := fieldAdd(bb, aa)
	return point{fieldMul(e, f), fieldMul(g, h), fieldMul(f, g), fieldMul(e, h)}
}

func scalarMultiply(scalar *big.Int, p point) point {
	result := identityPoint()
	addend := p
	for i := 0; i < scalar.BitLen(); i++ {
		if scalar.Bit(i) == 1 {
			result = addPoints(result, addend)
		}
		addend = addPoints(addend, addend)
	}
	return result
}

func encodePoint(p point) []byte {
	zInverse := new(big.Int).ModInverse(p.z, fieldPrime)
	x := fieldMul(p.x, zInverse)
	y := fieldMul(p.y, zInverse)
	encoded := new(big.Int).Set(y)
	if x.Bit(0) == 1 {
		encoded.SetBit(encoded, 255, 1)
	}
	return intLittleEndian(encoded, 32)
}

func strictPoint(encoded []byte) (point, error) {
	p, err := decodePoint(encoded)
	if err != nil {
		return point{}, err
	}
	if !bytes.Equal(encodePoint(p), encoded) || bytes.Equal(encoded, identityEncoding) || !bytes.Equal(encodePoint(scalarMultiply(groupOrder, p)), identityEncoding) {
		return point{}, ErrBadKey
	}
	return p, nil
}

func littleEndianInt(encoded []byte) *big.Int {
	reversed := make([]byte, len(encoded))
	for i := range encoded {
		reversed[len(encoded)-1-i] = encoded[i]
	}
	return new(big.Int).SetBytes(reversed)
}

func intLittleEndian(value *big.Int, size int) []byte {
	encoded := value.Bytes()
	result := make([]byte, size)
	for i := 0; i < len(encoded); i++ {
		result[i] = encoded[len(encoded)-1-i]
	}
	return result
}
