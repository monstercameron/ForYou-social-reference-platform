package content

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"testing"

	"foryou.local/foryou/internal/wire"
)

type mediaChunkReader struct {
	chunks [][]byte
	index  int
}

func (r *mediaChunkReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.index])
	r.chunks[r.index] = r.chunks[r.index][n:]
	if len(r.chunks[r.index]) == 0 {
		r.index++
	}
	return n, nil
}

type mediaFailReader struct {
	data []byte
}

type mediaNoReadReader struct{}

func (mediaNoReadReader) Read([]byte) (int, error) {
	panic("unexpected media read")
}

func (r *mediaFailReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func mediaSlot(size int64, digest, kind string) wire.MediaReference {
	return wire.MediaReference{SizeBytes: size, SHA256: digest, Kind: kind}
}

func mediaFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/media/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPrecheckMediaSize(t *testing.T) {
	t.Run("declared size above default limit", func(t *testing.T) {
		slot := mediaSlot(33<<20, "", "image")
		if err := PrecheckMediaSize(slot, DefaultMediaBudget()); !errors.Is(err, ErrMediaTooLarge) {
			t.Fatalf("PrecheckMediaSize() error = %v, want ErrMediaTooLarge", err)
		}
	})
	t.Run("undeclared size", func(t *testing.T) {
		if err := PrecheckMediaSize(mediaSlot(0, "", "image"), DefaultMediaBudget()); err != nil {
			t.Fatalf("PrecheckMediaSize() error = %v, want nil", err)
		}
	})
	t.Run("negative size", func(t *testing.T) {
		if err := PrecheckMediaSize(mediaSlot(-1, "", "image"), DefaultMediaBudget()); !errors.Is(err, ErrMediaTooLarge) {
			t.Fatalf("PrecheckMediaSize() error = %v, want ErrMediaTooLarge", err)
		}
	})
}

func TestCheckMediaResponse(t *testing.T) {
	check := func(t *testing.T, response *http.Response, slot wire.MediaReference, want error) {
		t.Helper()
		got := CheckMediaResponse(response, slot, MediaBudget{MaxBytes: 2048})
		if !errors.Is(got, want) {
			t.Fatalf("CheckMediaResponse() error = %v, want %v", got, want)
		}
	}
	t.Run("identity encoding", func(t *testing.T) {
		response := &http.Response{StatusCode: 200, ContentLength: 1000, Header: http.Header{"Content-Encoding": {"IDENTITY"}}}
		check(t, response, mediaSlot(1000, "", "image"), nil)
	})
	t.Run("absent encoding", func(t *testing.T) {
		check(t, &http.Response{StatusCode: 200, ContentLength: -1, Header: make(http.Header)}, mediaSlot(0, "", "image"), nil)
	})
	t.Run("encoded body", func(t *testing.T) {
		response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Encoding": {"gzip"}}}
		check(t, response, mediaSlot(0, "", "image"), ErrEncodedResponse)
	})
	t.Run("transport decompressed body", func(t *testing.T) {
		check(t, &http.Response{StatusCode: 200, Uncompressed: true, Header: make(http.Header)}, mediaSlot(0, "", "image"), ErrEncodedResponse)
	})
	t.Run("partial response", func(t *testing.T) {
		check(t, &http.Response{StatusCode: 206, Header: make(http.Header)}, mediaSlot(0, "", "image"), ErrMediaResponse)
	})
	t.Run("content length above declared size", func(t *testing.T) {
		check(t, &http.Response{StatusCode: 200, ContentLength: 2000, Header: make(http.Header)}, mediaSlot(1000, "", "image"), ErrMediaTooLarge)
	})
	t.Run("content length differs from declared size", func(t *testing.T) {
		check(t, &http.Response{StatusCode: 200, ContentLength: 999, Header: make(http.Header)}, mediaSlot(1000, "", "image"), ErrMediaSizeMismatch)
	})
}

func TestReadVerifiedMedia(t *testing.T) {
	zeros1000 := bytes.Repeat([]byte{0}, 1000)
	zeros900 := zeros1000[:900]
	t.Run("declared exact object across chunks", func(t *testing.T) {
		got, err := ReadVerifiedMedia(&mediaChunkReader{chunks: [][]byte{zeros1000[:400], zeros1000[400:]}}, mediaSlot(1000, "541b3e9daa09b20bf85fa273e5cbd3e80185aa4ec298e765db87742b70138a53", "image"), MediaBudget{MaxBytes: 1200})
		if err != nil || !bytes.Equal(got, zeros1000) {
			t.Fatalf("ReadVerifiedMedia() = %d bytes, %v; want exact 1000-byte object", len(got), err)
		}
	})
	t.Run("declared transfer exceeds limit", func(t *testing.T) {
		_, err := ReadVerifiedMedia(&mediaChunkReader{chunks: [][]byte{zeros1000[:400], append(append([]byte{}, zeros1000[400:]...), 0)}}, mediaSlot(1000, "541b3e9daa09b20bf85fa273e5cbd3e80185aa4ec298e765db87742b70138a53", "image"), MediaBudget{MaxBytes: 1200})
		if !errors.Is(err, ErrMediaTooLarge) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrMediaTooLarge", err)
		}
	})
	t.Run("declared transfer ends short", func(t *testing.T) {
		_, err := ReadVerifiedMedia(bytes.NewReader(zeros900), mediaSlot(1000, "541b3e9daa09b20bf85fa273e5cbd3e80185aa4ec298e765db87742b70138a53", "image"), MediaBudget{MaxBytes: 1200})
		if !errors.Is(err, ErrMediaSizeMismatch) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrMediaSizeMismatch", err)
		}
	})
	t.Run("undeclared bounded object", func(t *testing.T) {
		got, err := ReadVerifiedMedia(&mediaChunkReader{chunks: [][]byte{zeros900[:400], zeros900[400:]}}, mediaSlot(0, "fa807c957eafe34b850cb453a096df2e5899f0902a837fccd59f9aafa869fb44", "image"), MediaBudget{MaxBytes: 1000})
		if err != nil || !bytes.Equal(got, zeros900) {
			t.Fatalf("ReadVerifiedMedia() = %d bytes, %v; want exact 900-byte object", len(got), err)
		}
	})
	t.Run("undeclared transfer exceeds limit", func(t *testing.T) {
		_, err := ReadVerifiedMedia(bytes.NewReader(append(append([]byte{}, zeros1000...), 0)), mediaSlot(0, "", "image"), MediaBudget{MaxBytes: 1000})
		if !errors.Is(err, ErrMediaTooLarge) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrMediaTooLarge", err)
		}
	})
	t.Run("known digest", func(t *testing.T) {
		got, err := ReadVerifiedMedia(bytes.NewBufferString("abc"), mediaSlot(3, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "image"), MediaBudget{MaxBytes: 8})
		if err != nil || string(got) != "abc" {
			t.Fatalf("ReadVerifiedMedia() = %q, %v; want abc", got, err)
		}
	})
	t.Run("uppercase digest is noncanonical", func(t *testing.T) {
		_, err := ReadVerifiedMedia(bytes.NewBufferString("abc"), mediaSlot(3, "BA7816BF8F01CFEA414140DE5DAE2223B00361A396177A9CB410FF61F20015AD", "image"), MediaBudget{MaxBytes: 8})
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrDigestMismatch", err)
		}
	})
	t.Run("declared size above client cap refuses before reading", func(t *testing.T) {
		_, err := ReadVerifiedMedia(mediaNoReadReader{}, mediaSlot(1000, "", "image"), MediaBudget{MaxBytes: 500})
		if !errors.Is(err, ErrMediaTooLarge) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrMediaTooLarge", err)
		}
	})
	t.Run("different bytes", func(t *testing.T) {
		_, err := ReadVerifiedMedia(bytes.NewBufferString("abd"), mediaSlot(3, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "image"), MediaBudget{MaxBytes: 8})
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrDigestMismatch", err)
		}
	})
	t.Run("undeclared object with appended byte fails digest", func(t *testing.T) {
		_, err := ReadVerifiedMedia(bytes.NewBufferString("abc!"), mediaSlot(0, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "image"), MediaBudget{MaxBytes: 8})
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrDigestMismatch", err)
		}
	})
	t.Run("undeclared short object fails digest", func(t *testing.T) {
		_, err := ReadVerifiedMedia(bytes.NewBufferString("ab"), mediaSlot(0, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "image"), MediaBudget{MaxBytes: 8})
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrDigestMismatch", err)
		}
	})
	t.Run("unexpected EOF", func(t *testing.T) {
		_, err := ReadVerifiedMedia(&mediaFailReader{data: []byte("ab")}, mediaSlot(0, "", "image"), MediaBudget{MaxBytes: 8})
		if !errors.Is(err, ErrMediaIncomplete) {
			t.Fatalf("ReadVerifiedMedia() error = %v, want ErrMediaIncomplete", err)
		}
	})
}

func TestSniffMediaType(t *testing.T) {
	t.Run("JPEG signature", func(t *testing.T) { mediaSniff(t, []byte{0xff, 0xd8, 0xff}, MediaJPEG, nil) })
	t.Run("PNG fixture", func(t *testing.T) { mediaSniff(t, mediaFixture(t, "1x1.png"), MediaPNG, nil) })
	t.Run("GIF fixture", func(t *testing.T) { mediaSniff(t, mediaFixture(t, "three-frames.gif"), MediaGIF, nil) })
	t.Run("WebP fixture", func(t *testing.T) { mediaSniff(t, mediaFixture(t, "1x1.webp"), MediaWebP, nil) })
	t.Run("MP4 fixture", func(t *testing.T) { mediaSniff(t, mediaFixture(t, "ftyp-isom.mp4"), MediaMP4, nil) })
	t.Run("WebM fixture", func(t *testing.T) { mediaSniff(t, mediaFixture(t, "minimal.webm"), MediaWebM, nil) })
	t.Run("SVG is unsupported", func(t *testing.T) { mediaSniff(t, mediaFixture(t, "image.svg"), "", ErrUnsupportedMedia) })
	t.Run("HLS playlist is unsupported", func(t *testing.T) { mediaSniff(t, mediaFixture(t, "playlist.m3u8"), "", ErrUnsupportedMedia) })
	t.Run("HTML is unsupported", func(t *testing.T) { mediaSniff(t, []byte("<!doctype html>"), "", ErrUnsupportedMedia) })
	t.Run("AVIF brand is unsupported", func(t *testing.T) {
		mediaSniff(t, []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f'}, "", ErrUnsupportedMedia)
	})
	t.Run("truncated ftyp is unsupported", func(t *testing.T) {
		mediaSniff(t, []byte{0, 0, 0, 16, 'f', 't', 'y', 'p'}, "", ErrUnsupportedMedia)
	})
}

func mediaSniff(t *testing.T, input []byte, want MediaType, wantErr error) {
	t.Helper()
	got, err := SniffMediaType(input)
	if !errors.Is(err, wantErr) || got != want {
		t.Fatalf("SniffMediaType() = %q, %v; want %q, %v", got, err, want, wantErr)
	}
}

func TestCheckImageBudget(t *testing.T) {
	t.Run("one pixel PNG", func(t *testing.T) {
		w, h, frames, err := CheckImageBudget(mediaFixture(t, "1x1.png"), MediaPNG, DefaultMediaBudget())
		if err != nil || w != 1 || h != 1 || frames != 1 {
			t.Fatalf("CheckImageBudget() = %d×%d, %d frames, %v; want 1×1, 1 frame", w, h, frames, err)
		}
	})
	t.Run("oversized PNG dimensions", func(t *testing.T) {
		_, _, _, err := CheckImageBudget(mediaFixture(t, "huge-dimensions.png"), MediaPNG, DefaultMediaBudget())
		if !errors.Is(err, ErrMediaBudget) {
			t.Fatalf("CheckImageBudget() error = %v, want ErrMediaBudget", err)
		}
	})
	t.Run("three GIF frames", func(t *testing.T) {
		_, _, frames, err := CheckImageBudget(mediaFixture(t, "three-frames.gif"), MediaGIF, DefaultMediaBudget())
		if err != nil || frames != 3 {
			t.Fatalf("CheckImageBudget() frames = %d, %v; want 3, nil", frames, err)
		}
	})
	t.Run("GIF frame budget", func(t *testing.T) {
		budget := DefaultMediaBudget()
		budget.MaxFrames = 2
		_, _, _, err := CheckImageBudget(mediaFixture(t, "three-frames.gif"), MediaGIF, budget)
		if !errors.Is(err, ErrMediaBudget) {
			t.Fatalf("CheckImageBudget() error = %v, want ErrMediaBudget", err)
		}
	})
	t.Run("one pixel WebP", func(t *testing.T) {
		w, h, frames, err := CheckImageBudget(mediaFixture(t, "1x1.webp"), MediaWebP, DefaultMediaBudget())
		if err != nil || w != 1 || h != 1 || frames != 1 {
			t.Fatalf("CheckImageBudget() = %d×%d, %d frames, %v; want 1×1, 1 frame", w, h, frames, err)
		}
	})
	t.Run("APNG declares three frames", func(t *testing.T) {
		w, h, frames, err := CheckImageBudget(mediaAPNGWithFrames(mediaFixture(t, "1x1.png"), 3), MediaPNG, DefaultMediaBudget())
		if err != nil || w != 1 || h != 1 || frames != 3 {
			t.Fatalf("CheckImageBudget() = %d×%d, %d frames, %v; want 1×1, 3 frames", w, h, frames, err)
		}
	})
	t.Run("animated WebP counts ANMF chunks", func(t *testing.T) {
		data := mediaAnimatedWebP()
		w, h, frames, err := CheckImageBudget(data, MediaWebP, DefaultMediaBudget())
		if err != nil || w != 1 || h != 1 || frames != 3 {
			t.Fatalf("CheckImageBudget() = %d×%d, %d frames, %v; want 1×1, 3 frames", w, h, frames, err)
		}
	})
	t.Run("animated WebP frame budget", func(t *testing.T) {
		budget := DefaultMediaBudget()
		budget.MaxFrames = 2
		_, _, _, err := CheckImageBudget(mediaAnimatedWebP(), MediaWebP, budget)
		if !errors.Is(err, ErrMediaBudget) {
			t.Fatalf("CheckImageBudget() error = %v, want ErrMediaBudget", err)
		}
	})
}

func mediaAPNGWithFrames(png []byte, frames uint32) []byte {
	chunk := make([]byte, 16)
	binary.BigEndian.PutUint32(chunk[:4], 8)
	copy(chunk[4:8], "acTL")
	binary.BigEndian.PutUint32(chunk[8:12], frames)
	binary.BigEndian.PutUint32(chunk[12:16], crc32.ChecksumIEEE(chunk[4:12]))
	return append(append(append([]byte{}, png[:33]...), chunk...), png[33:]...)
}

// mediaAnimatedWebP is a 1×1 VP8X canvas followed by three empty ANMF chunks.
func mediaAnimatedWebP() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 46, 0, 0, 0, 'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X', 10, 0, 0, 0, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		'A', 'N', 'M', 'F', 0, 0, 0, 0,
		'A', 'N', 'M', 'F', 0, 0, 0, 0,
		'A', 'N', 'M', 'F', 0, 0, 0, 0,
	}
}

func TestVerifyMedia(t *testing.T) {
	data := mediaFixture(t, "1x1.png")
	const digest = "cba047489e7f7c7ef746c8ddaa3ed5d951c47bd3b8ccb7bb84b9ef8cbf165a34"
	t.Run("verified PNG", func(t *testing.T) {
		got, err := VerifyMedia(bytes.NewReader(data), mediaSlot(75, digest, "image"), DefaultMediaBudget())
		if err != nil || got.Type != MediaPNG || got.Width != 1 || got.Height != 1 || got.Frames != 1 || !bytes.Equal(got.Bytes, data) {
			t.Fatalf("VerifyMedia() = %#v, %v; want verified 1×1 PNG", got, err)
		}
	})
	t.Run("declared kind mismatch", func(t *testing.T) {
		_, err := VerifyMedia(bytes.NewReader(data), mediaSlot(75, digest, "video"), DefaultMediaBudget())
		if !errors.Is(err, ErrKindMismatch) {
			t.Fatalf("VerifyMedia() error = %v, want ErrKindMismatch", err)
		}
	})
	t.Run("transformed declared object exceeds size", func(t *testing.T) {
		_, err := VerifyMedia(bytes.NewReader(append(append([]byte{}, data...), 0)), mediaSlot(75, digest, "image"), DefaultMediaBudget())
		if !errors.Is(err, ErrMediaTooLarge) {
			t.Fatalf("VerifyMedia() error = %v, want ErrMediaTooLarge", err)
		}
	})
	t.Run("transformed undeclared object fails digest", func(t *testing.T) {
		_, err := VerifyMedia(bytes.NewReader(append(append([]byte{}, data...), 0)), mediaSlot(0, digest, "image"), DefaultMediaBudget())
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("VerifyMedia() error = %v, want ErrDigestMismatch", err)
		}
	})
}
