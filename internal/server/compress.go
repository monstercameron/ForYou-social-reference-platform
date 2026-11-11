package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

type compressedWriter struct {
	http.ResponseWriter
	encoding    string
	writer      io.WriteCloser
	wroteHeader bool
}

func (w *compressedWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.writer == nil {
		if w.encoding == "br" {
			w.writer = brotli.NewWriter(w.ResponseWriter)
		} else {
			w.writer = gzip.NewWriter(w.ResponseWriter)
		}
	}
	return w.writer.Write(p)
}
func (w *compressedWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(status)
}
func (w *compressedWriter) close() {
	if w.writer != nil {
		_ = w.writer.Close()
	}
}

// compress serves only GET responses through compression. Endpoints added
// later must assess whether they mix secrets and attacker-controlled content.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		acceptEncoding := strings.Join(r.Header.Values("Accept-Encoding"), ",")
		encoding, acceptable := chooseEncoding(acceptEncoding)
		if !acceptable {
			http.Error(w, "no acceptable content encoding", http.StatusNotAcceptable)
			return
		}
		// File ranges refer to the uncompressed bytes. Already-compressed media
		// also usually gains little from another compression pass.
		sensitive := hasSensitivePath(r.URL.Path)
		hasRange := r.Header.Get("Range") != ""
		if sensitive || hasRange || alreadyCompressedAsset(r.URL.Path) {
			_, identityAllowed := chooseEncoding(acceptEncoding + ",br;q=0,gzip;q=0")
			if sensitive && !identityAllowed {
				http.Error(w, "no acceptable content encoding", http.StatusNotAcceptable)
				return
			} else if identityAllowed {
				encoding = ""
			} else if hasRange {
				r = r.Clone(r.Context())
				r.Header.Del("Range")
				r.Header.Del("If-Range")
			}
		}
		switch encoding {
		case "br", "gzip":
			w.Header().Set("Content-Encoding", encoding)
			writer := &compressedWriter{ResponseWriter: w, encoding: encoding}
			defer writer.close()
			next.ServeHTTP(writer, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func hasSensitivePath(requestPath string) bool {
	for _, prefix := range []string{"/compose", "/quote", "/identity", "/wallet"} {
		if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
			return true
		}
	}
	return false
}

func alreadyCompressedAsset(requestPath string) bool {
	if !strings.HasPrefix(requestPath, "/static/") {
		return false
	}
	switch strings.ToLower(path.Ext(requestPath)) {
	case ".mp4", ".webm", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".woff2":
		return true
	default:
		return false
	}
}

func chooseEncoding(header string) (string, bool) {
	values := make(map[string]float64)
	for _, item := range strings.Split(header, ",") {
		parts := strings.Split(item, ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if name == "" {
			continue
		}
		quality := 1.0
		if len(parts) > 1 {
			param := strings.TrimSpace(parts[1])
			if !strings.HasPrefix(param, "q=") {
				continue
			}
			q, err := strconv.ParseFloat(strings.TrimPrefix(param, "q="), 64)
			if err != nil || q < 0 || q > 1 {
				continue
			}
			quality = q
		}
		values[name] = quality
	}
	quality := func(name string) float64 {
		if q, ok := values[name]; ok {
			return q
		}
		return values["*"]
	}
	br, gz := quality("br"), quality("gzip")
	if q, ok := values["identity"]; ok && q > br && q > gz {
		return "", true
	}
	if br > 0 && br >= gz {
		return "br", true
	}
	if gz > 0 {
		return "gzip", true
	}
	if q, ok := values["identity"]; ok && q == 0 {
		return "", false
	}
	if q, ok := values["*"]; ok && q == 0 {
		if _, explicit := values["identity"]; !explicit {
			return "", false
		}
	}
	return "", true
}
