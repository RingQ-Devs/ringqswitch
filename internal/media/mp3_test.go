package media

import (
	"os"
	"testing"
)

func TestDecodeMP3(t *testing.T) {
	path := os.Getenv("RINGQ_TEST_MP3")
	if path == "" {
		t.Skip("set RINGQ_TEST_MP3 to an MP3 file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !IsMP3(data) {
		t.Fatal("not recognised as MP3")
	}
	pcm, err := DecodeMP3(data)
	if err != nil || len(pcm) < 8000 {
		t.Fatalf("decoded %d samples: %v", len(pcm), err)
	}
	t.Logf("%.1f seconds", float64(len(pcm))/8000)
}
