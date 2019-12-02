package main

import (
	"bytes"
	"net"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDecodeASPath(t *testing.T) {
	tests := []struct {
		desc  string
		input []byte
		want  []asnSegment
	}{
		{
			desc:  "Test 1, AS_SEQUENCE",
			input: []byte{0x02, 0x02, 0x00, 0x00, 0x90, 0xec, 0x00, 0x00, 0x19, 0x35},
			want: []asnSegment{
				asnSegment{
					Type: 2,
					ASN:  37100,
				},
				asnSegment{
					Type: 2,
					ASN:  6453,
				},
			},
		},
		{
			desc:  "Test 2, AS_SET",
			input: []byte{0x01, 0x02, 0x00, 0x00, 0xcc, 0x8f, 0x00, 0x04, 0x06, 0x2e},
			want: []asnSegment{
				asnSegment{
					Type: 1,
					ASN:  52367,
				},
				asnSegment{
					Type: 1,
					ASN:  263726,
				},
			},
		},
	}

	for _, test := range tests {
		buf := bytes.NewBuffer(test.input)
		got := decodeASPath(buf)

		if !cmp.Equal(got, test.want) {
			t.Errorf("Test (%s): got %+v, want %+v", test.desc, got, test.want)
		}
	}
}

func TestDecodeNLRI(t *testing.T) {
	tests := []struct {
		desc  string
		input []byte
		want  []v4Addr
	}{
		{
			desc:  "test1",
			input: []byte{0x08, 0x39, 0x18, 0x9d, 0x96, 0x20, 0x10, 0x3a, 0x64, 0x20, 0x3a, 0x64, 0x64, 0x0},
			want: []v4Addr{
				v4Addr{
					Mask:   8,
					Prefix: net.IP{57},
				},
				v4Addr{
					Mask:   24,
					Prefix: net.IP{157, 150, 32},
				},
				v4Addr{
					Mask:   16,
					Prefix: net.IP{58, 100},
				},
				v4Addr{
					Mask:   32,
					Prefix: net.IP{58, 100, 100, 0},
				},
			},
		},
	}
	for _, test := range tests {
		buf := bytes.NewReader(test.input)
		got := decodeNLRI(buf)

		if !cmp.Equal(got, test.want) {
			t.Errorf("Test (%s): got %+v, want %+v", test.desc, got, test.want)
		}
	}
}

func TestDecodeAggregator(t *testing.T) {
	tests := []struct {
		desc    string
		input   []byte
		wantASN uint32
		wantIP  net.IP
	}{
		{
			desc:    "test1",
			input:   []byte{0x00, 0x00, 0x30, 0xa7, 0x3e, 0x18, 0x60, 0xa0},
			wantASN: 12455,
			wantIP:  net.IP{62, 24, 96, 160},
		},
	}

	for _, test := range tests {
		buf := bytes.NewBuffer(test.input)
		gotASN, gotIP := decodeAggregator(buf)

		if !cmp.Equal(gotASN, test.wantASN) {
			t.Errorf("Test (%s): got %+v, want %+v", test.desc, gotASN, test.wantASN)
		}
		if !cmp.Equal(gotIP, test.wantIP) {
			t.Errorf("Test (%s): got %+v, want %+v", test.desc, gotIP, test.wantIP)
		}
	}
}

func TestDecode4ByteNumber(t *testing.T) {
	tests := []struct {
		desc  string
		input []byte
		want  uint32
	}{
		{
			desc:  "test1",
			input: []byte{0x00, 0x00, 0x00, 0x00},
			want:  0,
		},
		{
			desc:  "test2",
			input: []byte{0xFF, 0xFF, 0xFF, 0xFF},
			want:  4294967295,
		},
		{
			desc:  "test3",
			input: []byte{0xFF, 0x0F, 0xFF, 0x0F},
			want:  4279238415,
		},
		{
			desc:  "test4",
			input: []byte{0x00, 0xFF, 0xFF, 0x00},
			want:  16776960,
		},
	}
	for _, test := range tests {
		buf := bytes.NewBuffer(test.input)
		got := decode4ByteNumber(buf)

		if !cmp.Equal(got, test.want) {
			t.Errorf("Test (%s): got %+v, want %+v", test.desc, got, test.want)
		}
	}
}
