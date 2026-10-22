package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"github.com/hajimehoshi/go-mp3"
)

// IsMP3 reports whether data looks like an MP3 file (an ID3 tag or an MPEG
// audio frame header).
func IsMP3(data []byte) bool {
	if len(data) >= 3 && string(data[:3]) == "ID3" {
		return true
	}
	return len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0
}

// DecodeMP3 decodes an MP3 file to 8 kHz mono samples.
func DecodeMP3(data []byte) ([]int16, error) {
	d, err := mp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	pcm, err := io.ReadAll(io.LimitReader(d, 200<<20))
	if err != nil {
		return nil, err
	}
	if len(pcm) < 4 {
		return nil, errors.New("mp3: no audio")
	}
	// 16-bit little-endian stereo.
	mono := make([]int16, len(pcm)/4)
	for i := range mono {
		l := int(int16(binary.LittleEndian.Uint16(pcm[i*4:])))
		r := int(int16(binary.LittleEndian.Uint16(pcm[i*4+2:])))
		mono[i] = int16((l + r) / 2)
	}
	return Resample(mono, d.SampleRate(), 8000), nil
}
