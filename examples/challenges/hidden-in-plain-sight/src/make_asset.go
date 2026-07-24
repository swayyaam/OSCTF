//go:build ignore

// make_asset generates a deterministic badge.png with the flag in a PNG tEXt
// chunk (exiftool/UserComment) and as a strings-visible trailer. Run by
// `make examples`; output is committed so seeding works without a build step.
//
//	go run make_asset.go
package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
)

const flag = "OSCTF{metadata_tells_all}"

func main() {
	// A small solid image — deterministic, no timestamps.
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 0x11, G: 0x16, B: 0x1f, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}

	// Insert a tEXt chunk (keyword "UserComment") just before IEND.
	out := insertText(buf.Bytes(), "UserComment", flag)
	// Append a strings-visible trailer after the PNG.
	out = append(out, []byte("\n# note: "+flag+"\n")...)

	if err := os.WriteFile("files/badge.png", out, 0o644); err != nil {
		panic(err)
	}
}

// insertText inserts a PNG tEXt chunk before the IEND chunk.
func insertText(pngBytes []byte, keyword, text string) []byte {
	iend := bytes.LastIndex(pngBytes, []byte("IEND"))
	if iend < 4 {
		panic("no IEND chunk")
	}
	insertAt := iend - 4 // start of the IEND length field

	data := append([]byte(keyword), 0)
	data = append(data, []byte(text)...)

	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(data)))
	typeAndData := append([]byte("tEXt"), data...)
	chunk.Write(typeAndData)
	_ = binary.Write(&chunk, binary.BigEndian, crc32.ChecksumIEEE(typeAndData))

	out := make([]byte, 0, len(pngBytes)+chunk.Len())
	out = append(out, pngBytes[:insertAt]...)
	out = append(out, chunk.Bytes()...)
	out = append(out, pngBytes[insertAt:]...)
	return out
}
