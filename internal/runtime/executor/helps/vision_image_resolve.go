package helps

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
)

const maxVisionImageBytes = 20 << 20

// ResolvedVisionImage is a normalized image payload ready for a future vision provider call.
type ResolvedVisionImage struct {
	Base64Data string
	MediaType  string
	SizeBytes  int
}

var allowedVisionImageMediaTypes = map[string]struct{}{
	"image/png":     {},
	"image/jpeg":    {},
	"image/gif":     {},
	"image/webp":    {},
	"image/bmp":     {},
	"image/svg+xml": {},
	"image/avif":    {},
}

var visionImageHTTPClient = http.DefaultClient

var visionImageMediaTypeByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".svg":  "image/svg+xml",
	".avif": "image/avif",
}

// ResolveVisionImage resolves supported image forms into base64 data with a checked media type.
func ResolveVisionImage(ctx context.Context, img oagmsg.ImageBlock) (ResolvedVisionImage, error) {
	if strings.TrimSpace(img.Data) != "" {
		return resolveVisionBase64(img.Data, img.MediaType)
	}

	rawURL := strings.TrimSpace(img.URL)
	if rawURL == "" {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: no image source")
	}
	if strings.HasPrefix(rawURL, "data:") {
		return resolveVisionDataURL(rawURL)
	}
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return resolveVisionHTTPImage(ctx, rawURL)
	}
	if strings.HasPrefix(rawURL, "file://") {
		path, errPath := fileURIPath(rawURL)
		if errPath != nil {
			return ResolvedVisionImage{}, errPath
		}
		return resolveVisionLocalImage(path)
	}
	if filepath.IsAbs(rawURL) {
		return resolveVisionLocalImage(rawURL)
	}
	return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: unsupported image URL or path")
}

func resolveVisionBase64(rawData string, mediaType string) (ResolvedVisionImage, error) {
	base64Data := strings.TrimSpace(rawData)
	if base64Data == "" {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: empty base64 data")
	}
	if len(base64Data) > base64.StdEncoding.EncodedLen(maxVisionImageBytes+1) {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: image exceeds %d bytes", maxVisionImageBytes)
	}
	decoded, errDecode := base64.StdEncoding.DecodeString(base64Data)
	if errDecode != nil {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: invalid base64 data")
	}
	if len(decoded) == 0 {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: empty decoded image")
	}
	if len(decoded) > maxVisionImageBytes {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: image exceeds %d bytes", maxVisionImageBytes)
	}
	checkedMediaType, errMedia := normalizeVisionMediaType(mediaType)
	if errMedia != nil {
		if strings.TrimSpace(mediaType) != "" {
			return ResolvedVisionImage{}, errMedia
		}
		checkedMediaType, errMedia = detectVisionMediaType(decoded, "")
		if errMedia != nil {
			return ResolvedVisionImage{}, errMedia
		}
	}
	return ResolvedVisionImage{Base64Data: base64Data, MediaType: checkedMediaType, SizeBytes: len(decoded)}, nil
}

func resolveVisionDataURL(rawURL string) (ResolvedVisionImage, error) {
	mediaType, base64Data, ok := parseVisionDataURL(rawURL)
	if !ok {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: invalid data URL")
	}
	return resolveVisionBase64(base64Data, mediaType)
}

func parseVisionDataURL(rawURL string) (mediaType, base64Data string, ok bool) {
	if !strings.HasPrefix(rawURL, "data:") {
		return "", "", false
	}
	meta, data, found := strings.Cut(strings.TrimPrefix(rawURL, "data:"), ",")
	if !found || strings.TrimSpace(data) == "" {
		return "", "", false
	}
	parts := strings.Split(meta, ";")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	base64Flag := false
	for _, part := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			base64Flag = true
			break
		}
	}
	if !base64Flag {
		return "", "", false
	}
	return parts[0], data, true
}

func resolveVisionHTTPImage(ctx context.Context, rawURL string) (ResolvedVisionImage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if errReq != nil {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: create HTTP request: %w", errReq)
	}
	resp, errHTTP := visionImageHTTPClient.Do(req)
	if errHTTP != nil {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: HTTP fetch: %w", errHTTP)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxVisionImageBytes {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: image exceeds %d bytes", maxVisionImageBytes)
	}

	declaredMediaType := resp.Header.Get("Content-Type")
	if strings.TrimSpace(declaredMediaType) != "" {
		if _, errMedia := normalizeVisionMediaType(declaredMediaType); errMedia != nil {
			return ResolvedVisionImage{}, errMedia
		}
	}

	data, errRead := readBounded(resp.Body, maxVisionImageBytes)
	if errRead != nil {
		return ResolvedVisionImage{}, errRead
	}
	mediaType, errMedia := normalizeVisionMediaType(declaredMediaType)
	if errMedia != nil {
		mediaType, errMedia = detectVisionMediaType(data, "")
		if errMedia != nil {
			return ResolvedVisionImage{}, errMedia
		}
	}
	return ResolvedVisionImage{Base64Data: base64.StdEncoding.EncodeToString(data), MediaType: mediaType, SizeBytes: len(data)}, nil
}

func resolveVisionLocalImage(path string) (ResolvedVisionImage, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: unsupported local path")
	}
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := visionImageMediaTypeByExt[ext]; !ok {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: unsupported image path extension %q", ext)
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: stat local file: %w", errStat)
	}
	if info.IsDir() {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: local image path is a directory")
	}
	if info.Size() > maxVisionImageBytes {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: image exceeds %d bytes", maxVisionImageBytes)
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return ResolvedVisionImage{}, fmt.Errorf("resolve vision image: open local file: %w", errOpen)
	}
	defer func() {
		_ = file.Close()
	}()
	data, errRead := readBounded(file, maxVisionImageBytes)
	if errRead != nil {
		return ResolvedVisionImage{}, errRead
	}
	mediaType, errMedia := detectVisionMediaType(data, ext)
	if errMedia != nil {
		return ResolvedVisionImage{}, errMedia
	}
	return ResolvedVisionImage{Base64Data: base64.StdEncoding.EncodeToString(data), MediaType: mediaType, SizeBytes: len(data)}, nil
}

func fileURIPath(rawURI string) (string, error) {
	parsed, errParse := url.Parse(rawURI)
	if errParse != nil {
		return "", fmt.Errorf("resolve vision image: invalid file URI: %w", errParse)
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("resolve vision image: unsupported file URI scheme")
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "", fmt.Errorf("resolve vision image: unsupported file URI host")
	}
	if parsed.Path == "" {
		return "", fmt.Errorf("resolve vision image: empty file URI path")
	}
	return parsed.Path, nil
}

func readBounded(r io.Reader, maxBytes int) ([]byte, error) {
	data, errRead := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if errRead != nil {
		return nil, fmt.Errorf("resolve vision image: read image: %w", errRead)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("resolve vision image: empty image content")
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("resolve vision image: image exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func detectVisionMediaType(data []byte, ext string) (string, error) {
	mediaType := http.DetectContentType(data)
	if _, ok := allowedVisionImageMediaTypes[mediaType]; ok {
		return mediaType, nil
	}
	if extMediaType, ok := visionImageMediaTypeByExt[strings.ToLower(ext)]; ok {
		return extMediaType, nil
	}
	return "", fmt.Errorf("resolve vision image: unsupported media type %q", mediaType)
}

func normalizeVisionMediaType(raw string) (string, error) {
	mediaType := strings.TrimSpace(raw)
	if mediaType == "" {
		return "", fmt.Errorf("resolve vision image: missing media type")
	}
	parsed, _, errParse := mime.ParseMediaType(mediaType)
	if errParse != nil {
		return "", fmt.Errorf("resolve vision image: invalid media type %q", raw)
	}
	parsed = strings.ToLower(strings.TrimSpace(parsed))
	if _, ok := allowedVisionImageMediaTypes[parsed]; !ok {
		return "", fmt.Errorf("resolve vision image: unsupported media type %q", parsed)
	}
	return parsed, nil
}
