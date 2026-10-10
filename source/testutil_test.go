package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"strconv"
	"testing"
	"time"
)

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mustInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

func makeTgz(t *testing.T, files []srcFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: int64(f.mode), Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func timeNow() time.Time { return time.Now().UTC() }
