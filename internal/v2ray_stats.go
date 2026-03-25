package internal

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// V2RayStatsAddr is the V2Ray Stats API listen address (matches sing-box experimental.v2ray_api.listen).
const V2RayStatsAddr = "127.0.0.1:8080"

const statsServiceMethod = "/v2ray.core.app.stats.command.StatsService/QueryStats"

// ── Raw proto codec (per-call, avoids proto.Message / full protobuf codegen) ─

type rawCodec struct{}

func (rawCodec) Marshal(v interface{}) ([]byte, error) {
	b, ok := v.(*rawBytes)
	if !ok {
		return nil, fmt.Errorf("rawCodec: expected *rawBytes, got %T", v)
	}
	return b.data, nil
}

func (rawCodec) Unmarshal(data []byte, v interface{}) error {
	b, ok := v.(*rawBytes)
	if !ok {
		return fmt.Errorf("rawCodec: expected *rawBytes, got %T", v)
	}
	b.data = append([]byte(nil), data...)
	return nil
}

func (rawCodec) Name() string { return "raw-proto" }

type rawBytes struct{ data []byte }

// ── Minimal protobuf wire encoding/decoding ─────────────────────────────────

// encodeQueryStatsRequest encodes QueryStatsRequest { string pattern = 1; }
func encodeQueryStatsRequest(pattern string) []byte {
	// field 1, wire type 2 (length-delimited) → tag = (1 << 3) | 2 = 0x0a
	buf := []byte{0x0a}
	buf = appendVarint(buf, uint64(len(pattern)))
	buf = append(buf, pattern...)
	return buf
}

// statEntry is a decoded V2Ray Stat { name, value }.
type statEntry struct {
	Name  string
	Value int64
}

// decodeQueryStatsResponse decodes QueryStatsResponse { repeated Stat stat = 1; }
// where Stat = { string name = 1; int64 value = 2; }
func decodeQueryStatsResponse(data []byte) ([]statEntry, error) {
	var stats []statEntry
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, fmt.Errorf("bad varint at response level")
		}
		data = data[n:]
		fieldNum := tag >> 3
		wireType := tag & 7

		if wireType != 2 {
			var ok bool
			if data, ok = skipField(data, wireType); !ok {
				return nil, fmt.Errorf("truncated field %d (wire type %d)", fieldNum, wireType)
			}
			continue
		}

		length, n := binary.Uvarint(data)
		if n <= 0 || uint64(len(data[n:])) < length {
			return nil, fmt.Errorf("bad length-delimited field")
		}
		data = data[n:]
		fieldData := data[:length]
		data = data[length:]

		if fieldNum == 1 {
			s, err := decodeStat(fieldData)
			if err != nil {
				return nil, fmt.Errorf("decode stat: %w", err)
			}
			stats = append(stats, s)
		}
	}
	return stats, nil
}

// decodeStat decodes Stat { string name = 1; int64 value = 2; }
func decodeStat(data []byte) (statEntry, error) {
	var s statEntry
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			return s, fmt.Errorf("bad varint in stat")
		}
		data = data[n:]
		fieldNum := tag >> 3
		wireType := tag & 7

		switch {
		case fieldNum == 1 && wireType == 2: // string name
			length, n := binary.Uvarint(data)
			if n <= 0 || uint64(len(data[n:])) < length {
				return s, fmt.Errorf("bad name length")
			}
			data = data[n:]
			s.Name = string(data[:length])
			data = data[length:]
		case fieldNum == 2 && wireType == 0: // int64 value
			v, n := binary.Uvarint(data)
			if n <= 0 {
				return s, fmt.Errorf("bad value varint")
			}
			data = data[n:]
			s.Value = int64(v)
		default:
			var ok bool
			if data, ok = skipField(data, wireType); !ok {
				return s, fmt.Errorf("truncated field %d (wire type %d)", fieldNum, wireType)
			}
		}
	}
	return s, nil
}

// skipField skips a protobuf field of the given wire type.
// Returns (remaining data, true) on success, or (nil, false) if data is truncated.
func skipField(data []byte, wireType uint64) ([]byte, bool) {
	switch wireType {
	case 0: // varint
		for len(data) > 0 {
			b := data[0]
			data = data[1:]
			if b&0x80 == 0 {
				return data, true
			}
		}
		return nil, false // unterminated varint
	case 1: // 64-bit fixed
		if len(data) < 8 {
			return nil, false
		}
		return data[8:], true
	case 2: // length-delimited
		length, n := binary.Uvarint(data)
		if n <= 0 || uint64(len(data[n:])) < length {
			return nil, false
		}
		return data[n+int(length):], true
	case 5: // 32-bit fixed
		if len(data) < 4 {
			return nil, false
		}
		return data[4:], true
	default:
		return nil, false // unknown wire type
	}
}

func appendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

// ── Public API ──────────────────────────────────────────────────────────────

// V2RayUserTraffic holds cumulative uplink/downlink bytes from V2Ray Stats API.
type V2RayUserTraffic struct {
	Uplink   int64
	Downlink int64
}

// StatsClient wraps a persistent gRPC connection to the V2Ray Stats API.
type StatsClient struct {
	conn *grpc.ClientConn
}

// NewStatsClient creates a persistent gRPC connection to the V2Ray Stats API.
func NewStatsClient(addr string) (*StatsClient, error) {
	if addr == "" {
		addr = V2RayStatsAddr
	}
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("v2ray stats dial: %w", err)
	}
	return &StatsClient{conn: conn}, nil
}

// Close closes the underlying gRPC connection.
func (c *StatsClient) Close() error {
	return c.conn.Close()
}

// QueryUserTraffic queries per-user cumulative traffic via V2Ray Stats gRPC API.
// Stats name format: "user>>>name>>>traffic>>>uplink" / "user>>>name>>>traffic>>>downlink"
func (c *StatsClient) QueryUserTraffic() (map[string]*V2RayUserTraffic, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &rawBytes{data: encodeQueryStatsRequest("user>>>")}
	resp := &rawBytes{}

	if err := c.conn.Invoke(ctx, statsServiceMethod, req, resp, grpc.ForceCodec(rawCodec{})); err != nil {
		return nil, fmt.Errorf("v2ray stats query: %w", err)
	}

	entries, err := decodeQueryStatsResponse(resp.data)
	if err != nil {
		return nil, fmt.Errorf("v2ray stats decode: %w", err)
	}

	result := make(map[string]*V2RayUserTraffic)
	for _, e := range entries {
		parts := strings.Split(e.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
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
	return result, nil
}
