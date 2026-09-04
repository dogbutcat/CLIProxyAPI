package helps

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/oagmsg"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func TestResolveVisionImageSupportedForms(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(tinyPNG)
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "image.png")
	if err := os.WriteFile(localPath, tinyPNG, 0600); err != nil {
		t.Fatal(err)
	}
	withVisionImageHTTPClient(t, visionImageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return visionHTTPResponse(http.StatusOK, "image/png", tinyPNG, -1), nil
	}))

	tests := []struct {
		name string
		img  oagmsg.ImageBlock
	}{
		{name: "base64", img: oagmsg.ImageBlock{Data: b64, MediaType: "image/png"}},
		{name: "data URL", img: oagmsg.ImageBlock{URL: "data:image/png;base64," + b64}},
		{name: "file URI", img: oagmsg.ImageBlock{URL: "file://" + localPath}},
		{name: "absolute path", img: oagmsg.ImageBlock{URL: localPath}},
		{name: "HTTP", img: oagmsg.ImageBlock{URL: "https://example.test/image.png"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveVisionImage(context.Background(), tt.img)
			if err != nil {
				t.Fatalf("ResolveVisionImage() error = %v", err)
			}
			if got.Base64Data == "" || got.MediaType != "image/png" || got.SizeBytes != len(tinyPNG) {
				t.Fatalf("resolved = %+v", got)
			}
		})
	}
}

func TestResolveVisionImageRejectsUnsupportedSourcesAndMedia(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("hello"))
	tmpDir := t.TempDir()
	txtPath := filepath.Join(tmpDir, "image.txt")
	if err := os.WriteFile(txtPath, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	largePath := filepath.Join(tmpDir, "large.png")
	largeFile, errCreate := os.Create(largePath)
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if errTruncate := largeFile.Truncate(maxVisionImageBytes + 1); errTruncate != nil {
		t.Fatal(errTruncate)
	}
	if errClose := largeFile.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	withVisionImageHTTPClient(t, visionImageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "text.example.test":
			return visionHTTPResponse(http.StatusOK, "text/plain", []byte("not an image"), -1), nil
		case "large.example.test":
			return visionHTTPResponse(http.StatusOK, "image/png", nil, maxVisionImageBytes+1), nil
		default:
			return visionHTTPResponse(http.StatusNotFound, "text/plain", nil, -1), nil
		}
	}))

	tests := []struct {
		name    string
		img     oagmsg.ImageBlock
		wantErr string
	}{
		{name: "relative path", img: oagmsg.ImageBlock{URL: "relative.png"}, wantErr: "unsupported image URL or path"},
		{name: "unsupported extension", img: oagmsg.ImageBlock{URL: txtPath}, wantErr: "unsupported image path extension"},
		{name: "unsupported data URL media", img: oagmsg.ImageBlock{URL: "data:text/plain;base64," + b64}, wantErr: "unsupported media type"},
		{name: "unsupported HTTP media", img: oagmsg.ImageBlock{URL: "https://text.example.test/image"}, wantErr: "unsupported media type"},
		{name: "oversized local file", img: oagmsg.ImageBlock{URL: largePath}, wantErr: "exceeds"},
		{name: "oversized HTTP content-length", img: oagmsg.ImageBlock{URL: "https://large.example.test/image"}, wantErr: "exceeds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveVisionImage(context.Background(), tt.img)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ResolveVisionImage() error = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveVisionImageHTTPInheritsContext(t *testing.T) {
	withVisionImageHTTPClient(t, visionImageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveVisionImage(ctx, oagmsg.ImageBlock{URL: "https://context.example.test/image"})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("ResolveVisionImage() error = %v, want context canceled", err)
	}
}

type visionImageRoundTripFunc func(*http.Request) (*http.Response, error)

func (f visionImageRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func withVisionImageHTTPClient(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	oldClient := visionImageHTTPClient
	visionImageHTTPClient = &http.Client{Transport: transport}
	t.Cleanup(func() { visionImageHTTPClient = oldClient })
	if visionImageHTTPClient.Timeout != 0 {
		t.Fatalf("vision image HTTP client timeout = %s, want zero", visionImageHTTPClient.Timeout)
	}
}

func visionHTTPResponse(status int, contentType string, body []byte, contentLength int) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	if contentLength >= 0 {
		header.Set("Content-Length", strconv.Itoa(contentLength))
	}
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		ContentLength: int64(contentLength),
		Body:          io.NopCloser(strings.NewReader(string(body))),
	}
}
