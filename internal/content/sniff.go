package content

import (
	"bytes"
	"encoding/binary"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"

	"foryou.local/foryou/internal/wire"
)

// MediaType identifies one supported inert media format.
type MediaType string

const (
	// MediaJPEG identifies JPEG images.
	MediaJPEG MediaType = "image/jpeg"
	// MediaPNG identifies PNG images.
	MediaPNG MediaType = "image/png"
	// MediaGIF identifies GIF images.
	MediaGIF MediaType = "image/gif"
	// MediaWebP identifies WebP images.
	MediaWebP MediaType = "image/webp"
	// MediaMP4 identifies supported MP4 video.
	MediaMP4 MediaType = "video/mp4"
	// MediaWebM identifies WebM video.
	MediaWebM MediaType = "video/webm"
)

var (
	mediaPNGSignature = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	mediaMP4Brands    = [][]byte{[]byte("isom"), []byte("iso2"), []byte("iso5"), []byte("iso6"), []byte("mp41"), []byte("mp42"), []byte("avc1"), []byte("M4V ")}
)

// SniffMediaType identifies a supported type from object bytes.
func SniffMediaType(b []byte) (MediaType, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return MediaJPEG, nil
	case bytes.HasPrefix(b, mediaPNGSignature):
		return MediaPNG, nil
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return MediaGIF, nil
	case len(b) >= 16 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")) && (bytes.Equal(b[12:16], []byte("VP8 ")) || bytes.Equal(b[12:16], []byte("VP8L")) || bytes.Equal(b[12:16], []byte("VP8X"))):
		return MediaWebP, nil
	case len(b) >= 12 && bytes.Equal(b[4:8], []byte("ftyp")) && mediaHasMP4Brand(b[8:12]):
		return MediaMP4, nil
	case len(b) >= 4 && bytes.Equal(b[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) && mediaWebMDocType(b):
		return MediaWebM, nil
	default:
		return "", ErrUnsupportedMedia
	}
}

func mediaHasMP4Brand(brand []byte) bool {
	for _, allowed := range mediaMP4Brands {
		if bytes.Equal(brand, allowed) {
			return true
		}
	}
	return false
}

func mediaWebMDocType(b []byte) bool {
	if len(b) < 5 {
		return false
	}
	position := 4
	headerSize, sizeLength, ok := mediaEBMLVInt(b, position)
	if !ok {
		return false
	}
	position += sizeLength
	if headerSize > uint64(len(b)-position) {
		return false
	}
	headerEnd := position + int(headerSize)
	for position < headerEnd {
		id, idLength, valid := mediaEBMLID(b[:headerEnd], position)
		if !valid {
			return false
		}
		position += idLength
		size, sizeLength, valid := mediaEBMLVInt(b[:headerEnd], position)
		if !valid || size > uint64(headerEnd-position-sizeLength) {
			return false
		}
		position += sizeLength
		if id == 0x4282 {
			return bytes.Equal(b[position:position+int(size)], []byte("webm"))
		}
		position += int(size)
	}
	return false
}

func mediaEBMLID(b []byte, position int) (uint64, int, bool) {
	_, length, ok := mediaEBMLVInt(b, position)
	if !ok {
		return 0, 0, false
	}
	var id uint64
	for _, octet := range b[position : position+length] {
		id = id<<8 | uint64(octet)
	}
	return id, length, true
}

func mediaEBMLVInt(b []byte, position int) (uint64, int, bool) {
	if position >= len(b) || b[position] == 0 {
		return 0, 0, false
	}
	mask := byte(0x80)
	length := 1
	for b[position]&mask == 0 {
		mask >>= 1
		length++
	}
	if length > 8 || position+length > len(b) {
		return 0, 0, false
	}
	value := uint64(b[position] & (mask - 1))
	for i := 1; i < length; i++ {
		value = value<<8 | uint64(b[position+i])
	}
	return value, length, true
}

// CheckImageBudget returns image dimensions and frame count without decoding pixels.
func CheckImageBudget(b []byte, t MediaType, budget MediaBudget) (width, height, frames int, err error) {
	frames = 1
	switch t {
	case MediaJPEG:
		config, err := jpeg.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return 0, 0, 0, ErrUnsupportedMedia
		}
		width, height = config.Width, config.Height
	case MediaPNG:
		config, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return 0, 0, 0, ErrUnsupportedMedia
		}
		width, height = config.Width, config.Height
		if apngFrames, found, valid := mediaAPNGFrames(b); !valid {
			return 0, 0, 0, ErrUnsupportedMedia
		} else if found {
			frames = apngFrames
		}
	case MediaGIF:
		config, err := gif.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return 0, 0, 0, ErrUnsupportedMedia
		}
		width, height = config.Width, config.Height
		frames, err = mediaGIFFrames(b)
		if err != nil {
			return 0, 0, 0, ErrUnsupportedMedia
		}
	case MediaWebP:
		var err error
		width, height, frames, err = mediaWebPDimensions(b)
		if err != nil {
			return 0, 0, 0, ErrUnsupportedMedia
		}
	default:
		return 0, 0, 0, ErrUnsupportedMedia
	}
	if width <= 0 || height <= 0 || frames <= 0 {
		return 0, 0, 0, ErrUnsupportedMedia
	}
	if width > budget.MaxWidth || height > budget.MaxHeight || int64(width) > budget.MaxPixels/int64(height) || frames > budget.MaxFrames {
		return width, height, frames, ErrMediaBudget
	}
	return width, height, frames, nil
}

func mediaGIFFrames(b []byte) (int, error) {
	if len(b) < 13 || (!bytes.Equal(b[:6], []byte("GIF87a")) && !bytes.Equal(b[:6], []byte("GIF89a"))) {
		return 0, ErrUnsupportedMedia
	}
	position := 13
	if b[10]&0x80 != 0 {
		position += 3 * (1 << ((b[10] & 0x07) + 1))
	}
	if position > len(b) {
		return 0, ErrUnsupportedMedia
	}
	frames := 0
	for position < len(b) {
		marker := b[position]
		position++
		switch marker {
		case 0x3b:
			return frames, nil
		case 0x21:
			if position >= len(b) {
				return 0, ErrUnsupportedMedia
			}
			position++
			var ok bool
			position, ok = mediaGIFSubBlocks(b, position)
			if !ok {
				return 0, ErrUnsupportedMedia
			}
		case 0x2c:
			if position+9 > len(b) {
				return 0, ErrUnsupportedMedia
			}
			packed := b[position+8]
			position += 9
			if packed&0x80 != 0 {
				position += 3 * (1 << ((packed & 0x07) + 1))
			}
			if position >= len(b) {
				return 0, ErrUnsupportedMedia
			}
			position++
			var ok bool
			position, ok = mediaGIFSubBlocks(b, position)
			if !ok {
				return 0, ErrUnsupportedMedia
			}
			frames++
		default:
			return 0, ErrUnsupportedMedia
		}
	}
	return 0, ErrUnsupportedMedia
}

func mediaGIFSubBlocks(b []byte, position int) (int, bool) {
	for position < len(b) {
		size := int(b[position])
		position++
		if size == 0 {
			return position, true
		}
		if position+size > len(b) {
			return 0, false
		}
		position += size
	}
	return 0, false
}

func mediaAPNGFrames(b []byte) (int, bool, bool) {
	position := 8
	for position+12 <= len(b) {
		length := binary.BigEndian.Uint32(b[position : position+4])
		if uint64(length)+12 > uint64(len(b)-position) {
			return 0, false, false
		}
		chunkType := b[position+4 : position+8]
		if bytes.Equal(chunkType, []byte("acTL")) {
			if length != 8 {
				return 0, false, false
			}
			count := binary.BigEndian.Uint32(b[position+8 : position+12])
			if count == 0 || uint64(count) > uint64(^uint(0)>>1) {
				return 0, false, false
			}
			return int(count), true, true
		}
		position += int(length) + 12
		if bytes.Equal(chunkType, []byte("IEND")) {
			break
		}
	}
	return 1, false, true
}

func mediaWebPDimensions(b []byte) (int, int, int, error) {
	if len(b) < 16 || !bytes.Equal(b[:4], []byte("RIFF")) || !bytes.Equal(b[8:12], []byte("WEBP")) {
		return 0, 0, 0, ErrUnsupportedMedia
	}
	position := 12
	width, height, frames := 0, 0, 0
	for position < len(b) {
		if len(b)-position < 8 {
			return 0, 0, 0, ErrUnsupportedMedia
		}
		chunkType := b[position : position+4]
		length := uint64(binary.LittleEndian.Uint32(b[position+4 : position+8]))
		data := position + 8
		if length > uint64(len(b)-data) {
			return 0, 0, 0, ErrUnsupportedMedia
		}
		if position == 12 {
			switch string(chunkType) {
			case "VP8X":
				if length < 10 {
					return 0, 0, 0, ErrUnsupportedMedia
				}
				width = 1 + int(b[data+4]) + int(b[data+5])<<8 + int(b[data+6])<<16
				height = 1 + int(b[data+7]) + int(b[data+8])<<8 + int(b[data+9])<<16
			case "VP8L":
				if length < 5 || b[data] != 0x2f {
					return 0, 0, 0, ErrUnsupportedMedia
				}
				width = 1 + int(b[data+1]) + (int(b[data+2])&0x3f)<<8
				height = 1 + (int(b[data+2]) >> 6) + int(b[data+3])<<2 + (int(b[data+4])&0x0f)<<10
			case "VP8 ":
				if length < 10 || !bytes.Equal(b[data+3:data+6], []byte{0x9d, 0x01, 0x2a}) {
					return 0, 0, 0, ErrUnsupportedMedia
				}
				width = int(binary.LittleEndian.Uint16(b[data+6:data+8]) & 0x3fff)
				height = int(binary.LittleEndian.Uint16(b[data+8:data+10]) & 0x3fff)
			default:
				return 0, 0, 0, ErrUnsupportedMedia
			}
		} else if string(chunkType) == "ANMF" {
			frames++
		}
		chunkBytes := length + length%2
		if chunkBytes > uint64(len(b)-data) {
			return 0, 0, 0, ErrUnsupportedMedia
		}
		position = data + int(chunkBytes)
	}
	if width == 0 || height == 0 {
		return 0, 0, 0, ErrUnsupportedMedia
	}
	if frames == 0 {
		frames = 1
	}
	return width, height, frames, nil
}

// VerifyMedia verifies complete bytes before sniffing or inspecting the media.
func VerifyMedia(body io.Reader, slot wire.MediaReference, b MediaBudget) (VerifiedMedia, error) {
	data, err := ReadVerifiedMedia(body, slot, b)
	if err != nil {
		return VerifiedMedia{}, err
	}
	t, err := SniffMediaType(data)
	if err != nil {
		return VerifiedMedia{}, err
	}
	if (strings.HasPrefix(string(t), "image/") && slot.Kind != "image") || (strings.HasPrefix(string(t), "video/") && slot.Kind != "video") {
		return VerifiedMedia{}, ErrKindMismatch
	}
	verified := VerifiedMedia{Bytes: data, Type: t}
	if strings.HasPrefix(string(t), "image/") {
		verified.Width, verified.Height, verified.Frames, err = CheckImageBudget(data, t, b)
		if err != nil {
			return VerifiedMedia{}, err
		}
	}
	return verified, nil
}
