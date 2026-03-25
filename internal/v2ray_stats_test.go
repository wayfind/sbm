package internal

import (
	"encoding/binary"
	"testing"
)

// ── encodeQueryStatsRequest ─────────────────────────────────────────────────

func TestEncodeQueryStatsRequest(t *testing.T) {
	data := encodeQueryStatsRequest("user>>>")
	// tag: 0x0a (field 1, wire type 2), then varint length of "user>>>", then the string
	if len(data) == 0 || data[0] != 0x0a {
		t.Fatalf("expected tag 0x0a, got %x", data[0])
	}
	length, n := binary.Uvarint(data[1:])
	if n <= 0 {
		t.Fatal("bad varint for string length")
	}
	if int(length) != len("user>>>") {
		t.Fatalf("expected length %d, got %d", len("user>>>"), length)
	}
	got := string(data[1+n:])
	if got != "user>>>" {
		t.Fatalf("expected %q, got %q", "user>>>", got)
	}
}

func TestEncodeQueryStatsRequestEmpty(t *testing.T) {
	data := encodeQueryStatsRequest("")
	// tag + varint(0) → just 2 bytes: 0x0a 0x00
	if len(data) != 2 || data[0] != 0x0a || data[1] != 0x00 {
		t.Fatalf("unexpected encoding for empty pattern: %x", data)
	}
}

// ── protobuf helpers ────────────────────────────────────────────────────────

// buildProtoVarint encodes a uint64 as a protobuf varint.
func buildProtoVarint(v uint64) []byte {
	return appendVarint(nil, v)
}

// buildProtoTag creates a protobuf field tag.
func buildProtoTag(fieldNum uint64, wireType uint64) []byte {
	return buildProtoVarint(fieldNum<<3 | wireType)
}

// buildProtoLengthDelimited creates a length-delimited protobuf field.
func buildProtoLengthDelimited(fieldNum uint64, data []byte) []byte {
	buf := buildProtoTag(fieldNum, 2)
	buf = appendVarint(buf, uint64(len(data)))
	buf = append(buf, data...)
	return buf
}

// buildProtoString creates a string protobuf field.
func buildProtoString(fieldNum uint64, s string) []byte {
	return buildProtoLengthDelimited(fieldNum, []byte(s))
}

// buildProtoVarintField creates a varint protobuf field.
func buildProtoVarintField(fieldNum uint64, v uint64) []byte {
	buf := buildProtoTag(fieldNum, 0)
	buf = appendVarint(buf, v)
	return buf
}

// buildStat encodes a V2Ray Stat message { string name = 1; int64 value = 2; }
func buildStat(name string, value int64) []byte {
	var inner []byte
	inner = append(inner, buildProtoString(1, name)...)
	inner = append(inner, buildProtoVarintField(2, uint64(value))...)
	return inner
}

// buildQueryStatsResponse encodes a QueryStatsResponse { repeated Stat stat = 1; }
func buildQueryStatsResponse(stats ...[]byte) []byte {
	var buf []byte
	for _, s := range stats {
		buf = append(buf, buildProtoLengthDelimited(1, s)...)
	}
	return buf
}

// ── decodeStat ──────────────────────────────────────────────────────────────

func TestDecodeStat(t *testing.T) {
	data := buildStat("user>>>alice>>>traffic>>>uplink", 12345)
	s, err := decodeStat(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "user>>>alice>>>traffic>>>uplink" {
		t.Fatalf("name = %q, want %q", s.Name, "user>>>alice>>>traffic>>>uplink")
	}
	if s.Value != 12345 {
		t.Fatalf("value = %d, want 12345", s.Value)
	}
}

func TestDecodeStatZeroValue(t *testing.T) {
	// Only name, no value field → value should be zero
	data := buildProtoString(1, "user>>>bob>>>traffic>>>downlink")
	s, err := decodeStat(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "user>>>bob>>>traffic>>>downlink" {
		t.Fatalf("name = %q", s.Name)
	}
	if s.Value != 0 {
		t.Fatalf("value = %d, want 0", s.Value)
	}
}

func TestDecodeStatEmpty(t *testing.T) {
	s, err := decodeStat(nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "" || s.Value != 0 {
		t.Fatalf("expected zero stat, got %+v", s)
	}
}

func TestDecodeStatUnknownField(t *testing.T) {
	// field 1 (name) + field 99 (unknown varint) + field 2 (value)
	var data []byte
	data = append(data, buildProtoString(1, "test")...)
	data = append(data, buildProtoVarintField(99, 42)...)
	data = append(data, buildProtoVarintField(2, 100)...)
	s, err := decodeStat(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "test" || s.Value != 100 {
		t.Fatalf("got %+v", s)
	}
}

// ── decodeQueryStatsResponse ────────────────────────────────────────────────

func TestDecodeQueryStatsResponse(t *testing.T) {
	resp := buildQueryStatsResponse(
		buildStat("user>>>alice>>>traffic>>>uplink", 1000),
		buildStat("user>>>alice>>>traffic>>>downlink", 2000),
		buildStat("user>>>bob>>>traffic>>>uplink", 500),
	)
	stats, err := decodeQueryStatsResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 3 {
		t.Fatalf("got %d stats, want 3", len(stats))
	}

	expected := []statEntry{
		{"user>>>alice>>>traffic>>>uplink", 1000},
		{"user>>>alice>>>traffic>>>downlink", 2000},
		{"user>>>bob>>>traffic>>>uplink", 500},
	}
	for i, e := range expected {
		if stats[i].Name != e.Name || stats[i].Value != e.Value {
			t.Errorf("stat[%d] = %+v, want %+v", i, stats[i], e)
		}
	}
}

func TestDecodeQueryStatsResponseEmpty(t *testing.T) {
	stats, err := decodeQueryStatsResponse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Fatalf("got %d stats, want 0", len(stats))
	}
}

func TestDecodeQueryStatsResponseUnknownField(t *testing.T) {
	// Unknown field 2 (varint) + known field 1 (stat)
	var data []byte
	data = append(data, buildProtoVarintField(2, 999)...)
	data = append(data, buildProtoLengthDelimited(1, buildStat("user>>>x>>>traffic>>>uplink", 1))...)
	stats, err := decodeQueryStatsResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Name != "user>>>x>>>traffic>>>uplink" {
		t.Fatalf("got %+v", stats)
	}
}

// ── skipField ───────────────────────────────────────────────────────────────

func TestSkipFieldVarint(t *testing.T) {
	// Encode a varint (300 = 0xAC 0x02) followed by a sentinel byte
	data := []byte{0xAC, 0x02, 0xFF}
	rest, ok := skipField(data, 0)
	if !ok {
		t.Fatal("skipField varint failed")
	}
	if len(rest) != 1 || rest[0] != 0xFF {
		t.Fatalf("rest = %x, want [ff]", rest)
	}
}

func TestSkipFieldVarintTruncated(t *testing.T) {
	// Unterminated varint (high bit always set)
	data := []byte{0x80, 0x80}
	_, ok := skipField(data, 0)
	if ok {
		t.Fatal("expected failure on truncated varint")
	}
}

func TestSkipField64Bit(t *testing.T) {
	data := make([]byte, 9)
	data[8] = 0xFF // sentinel
	rest, ok := skipField(data, 1)
	if !ok || len(rest) != 1 || rest[0] != 0xFF {
		t.Fatalf("64-bit skip: ok=%v rest=%x", ok, rest)
	}
}

func TestSkipField64BitTruncated(t *testing.T) {
	data := make([]byte, 7)
	_, ok := skipField(data, 1)
	if ok {
		t.Fatal("expected failure on truncated 64-bit")
	}
}

func TestSkipFieldLengthDelimited(t *testing.T) {
	// length=3 + 3 bytes + sentinel
	data := append(buildProtoVarint(3), []byte{0x01, 0x02, 0x03, 0xFF}...)
	rest, ok := skipField(data, 2)
	if !ok || len(rest) != 1 || rest[0] != 0xFF {
		t.Fatalf("LD skip: ok=%v rest=%x", ok, rest)
	}
}

func TestSkipFieldLengthDelimitedTruncated(t *testing.T) {
	// length=10 but only 3 bytes of data
	data := append(buildProtoVarint(10), []byte{0x01, 0x02, 0x03}...)
	_, ok := skipField(data, 2)
	if ok {
		t.Fatal("expected failure on truncated length-delimited")
	}
}

func TestSkipField32Bit(t *testing.T) {
	data := []byte{0, 0, 0, 0, 0xFF}
	rest, ok := skipField(data, 5)
	if !ok || len(rest) != 1 || rest[0] != 0xFF {
		t.Fatalf("32-bit skip: ok=%v rest=%x", ok, rest)
	}
}

func TestSkipField32BitTruncated(t *testing.T) {
	data := make([]byte, 3)
	_, ok := skipField(data, 5)
	if ok {
		t.Fatal("expected failure on truncated 32-bit")
	}
}

func TestSkipFieldUnknownWireType(t *testing.T) {
	_, ok := skipField([]byte{0x01}, 3)
	if ok {
		t.Fatal("expected failure on unknown wire type")
	}
}

// ── decodeStat with truncated data ──────────────────────────────────────────

func TestDecodeStatTruncatedString(t *testing.T) {
	// String field with length=100 but only 3 bytes of actual data
	data := buildProtoTag(1, 2)
	data = appendVarint(data, 100)
	data = append(data, []byte("abc")...)
	_, err := decodeStat(data)
	if err == nil {
		t.Fatal("expected error on truncated string")
	}
}

func TestDecodeQueryStatsResponseTruncated(t *testing.T) {
	// Length-delimited field with length=100 but only 3 bytes
	data := buildProtoTag(1, 2)
	data = appendVarint(data, 100)
	data = append(data, []byte("abc")...)
	_, err := decodeQueryStatsResponse(data)
	if err == nil {
		t.Fatal("expected error on truncated response")
	}
}

// ── Large value (multi-byte varint) ─────────────────────────────────────────

func TestDecodeStatLargeValue(t *testing.T) {
	// 1 TB = 1099511627776
	data := buildStat("user>>>heavy>>>traffic>>>downlink", 1099511627776)
	s, err := decodeStat(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.Value != 1099511627776 {
		t.Fatalf("value = %d, want 1099511627776", s.Value)
	}
}

// ── parseStats (integration: decode → name parsing) ─────────────────────────

func TestParseStatsFromResponse(t *testing.T) {
	resp := buildQueryStatsResponse(
		buildStat("user>>>alice>>>traffic>>>uplink", 1000),
		buildStat("user>>>alice>>>traffic>>>downlink", 2000),
		buildStat("user>>>bob>>>traffic>>>uplink", 500),
		buildStat("user>>>bob>>>traffic>>>downlink", 800),
		buildStat("inbound>>>vless-in>>>traffic>>>uplink", 9999), // should be ignored
	)
	entries, err := decodeQueryStatsResponse(resp)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the parsing logic from QueryUserTraffic
	result := make(map[string]*V2RayUserTraffic)
	for _, e := range entries {
		parts := splitStatName(e.Name)
		if parts == nil {
			continue
		}
		name := parts[1]
		if _, ok := result[name]; !ok {
			result[name] = &V2RayUserTraffic{}
		}
		switch parts[3] {
		case "uplink":
			result[name].Uplink = e.Value
		case "downlink":
			result[name].Downlink = e.Value
		}
	}

	if len(result) != 2 {
		t.Fatalf("got %d users, want 2", len(result))
	}
	if result["alice"].Uplink != 1000 || result["alice"].Downlink != 2000 {
		t.Errorf("alice = %+v", result["alice"])
	}
	if result["bob"].Uplink != 500 || result["bob"].Downlink != 800 {
		t.Errorf("bob = %+v", result["bob"])
	}
}

// splitStatName is a test helper replicating the parsing in QueryUserTraffic.
func splitStatName(name string) []string {
	parts := make([]string, 0, 4)
	for _, p := range split4(name) {
		parts = append(parts, p)
	}
	if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
		return nil
	}
	return parts
}

func split4(s string) [4]string {
	var result [4]string
	idx := 0
	for i := 0; i < 4; i++ {
		sep := indexOf(s[idx:], ">>>")
		if sep < 0 {
			if i == 3 {
				result[i] = s[idx:]
				return result
			}
			return [4]string{}
		}
		result[i] = s[idx : idx+sep]
		idx += sep + 3
	}
	return result
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
