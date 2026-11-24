package exectestkit

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func scanFrame(typ byte, flags byte, stream uint32, payload []byte) []byte {
	n := len(payload)
	head := []byte{byte(n >> 16), byte(n >> 8), byte(n), typ, flags, byte(stream >> 24), byte(stream >> 16), byte(stream >> 8), byte(stream)}
	return append(head, payload...)
}

func goAwayPayload(last, code uint32, debug string) []byte {
	p := []byte{byte(last >> 24), byte(last >> 16), byte(last >> 8), byte(last), byte(code >> 24), byte(code >> 16), byte(code >> 8), byte(code)}
	return append(p, debug...)
}

// describe is a frame as the scanner reports it, for comparison.
func describe(f Frame) string {
	return fmt.Sprintf("type=%#x ack=%v code=%#x debug=%q", uint8(f.Type), f.Ack, f.ErrCode, f.Debug)
}

// The scanner finds the PINGs and GOAWAYs of a stream of HTTP/2 however the stream is cut into reads: a connection delivers it in
// pieces of any size, and a frame, a header, even the preface can straddle two of them. What is not a PING or a GOAWAY, whatever its
// size, is passed over and cannot hide the next frame.
func TestFrameScannerFindsPingsAndGoAwaysWhereverTheStreamIsCut(t *testing.T) {
	ping := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	stream := bytes.Join([][]byte{
		[]byte(clientPreface),
		scanFrame(0x4, 0, 0, nil),                 // SETTINGS, empty
		scanFrame(0x6, 0, 0, ping),                // PING
		scanFrame(0x0, 0, 1, make([]byte, 40000)), // DATA, larger than any piece below
		scanFrame(0x6, 0x1, 0, ping),              // PING ack
		scanFrame(0x8, 0, 0, []byte{0, 0, 1, 0}),  // WINDOW_UPDATE
		scanFrame(0x7, 0, 0, goAwayPayload(5, 0xb, "too_many_pings")),
		scanFrame(0x7, 0, 0, goAwayPayload(0x01020304, 0x0a0b0c0d, "x")),     // every byte of the stream id and of the code differs
		scanFrame(0x7, 0, 0, goAwayPayload(0, 0, strings.Repeat("x", 5000))), // a GOAWAY whose debug data exceeds what the scanner keeps
		scanFrame(0x6, 0, 0, ping),
	}, nil)
	want := []string{
		"type=0x6 ack=false code=0x0 debug=\"\"",
		"type=0x6 ack=true code=0x0 debug=\"\"",
		"type=0x7 ack=false code=0xb debug=\"too_many_pings\"",
		"type=0x7 ack=false code=0xa0b0c0d debug=\"x\"",
		fmt.Sprintf("type=0x7 ack=false code=0x0 debug=%q", strings.Repeat("x", maxKept-8)),
		"type=0x6 ack=false code=0x0 debug=\"\"",
	}
	for _, piece := range []int{1, 2, 3, 5, 8, 9, 10, 13, 24, 25, 100, 4096, len(stream)} {
		scan := frameScanner{preface: len(clientPreface)}
		var got []string
		for rest := stream; len(rest) > 0; {
			n := min(piece, len(rest))
			scan.feed(rest[:n], func(f Frame) { got = append(got, describe(f)) })
			rest = rest[n:]
		}
		if !slices.Equal(got, want) {
			t.Errorf("read in pieces of %d bytes, the scanner reported\n  %s\nwant\n  %s", piece, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}

// The direction that carries no preface starts at the first frame.
func TestFrameScannerWithNoPrefaceStartsAtTheFirstFrame(t *testing.T) {
	scan := frameScanner{}
	var got []string
	scan.feed(scanFrame(0x6, 0, 0, []byte{1, 2, 3, 4, 5, 6, 7, 8}), func(f Frame) { got = append(got, describe(f)) })
	if len(got) != 1 {
		t.Errorf("reported %v, want one PING", got)
	}
}
