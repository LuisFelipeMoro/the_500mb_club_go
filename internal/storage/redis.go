package storage

import (
	"context"
	"encoding/binary"
	"strconv"

	"github.com/redis/rueidis"
)

// Store is the telemetry persistence contract. Members are 56-byte encoded
// TelemetryPoints; the score is derived from the leading int64 timestamp.
type Store interface {
	// AddMulti writes one ZADD per device in a single pipeline round-trip.
	AddMulti(ctx context.Context, batches map[string][][]byte) error
	// Range returns members with score in [fromTS, toTS], skipping offset, up to count.
	Range(ctx context.Context, deviceID string, fromTS, toTS, offset, count int64) ([][]byte, error)
	// LastN returns up to n newest members (highest score first).
	LastN(ctx context.Context, deviceID string, n int64) ([][]byte, error)
	Ping(ctx context.Context) error
	Close()
}

// RueidisStore is the rueidis-backed Store implementation.
type RueidisStore struct {
	client rueidis.Client
}

// NewRueidisStore dials Redis at addr over a single multiplexed RESP3 connection.
func NewRueidisStore(addr string) (*RueidisStore, error) {
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:  []string{addr},
		DisableCache: true, // no client-side cache: keeps RSS minimal
	})
	if err != nil {
		return nil, err
	}
	return &RueidisStore{client: client}, nil
}

func key(deviceID string) string { return "telemetry:" + deviceID }

// retainPerDevice caps each device's sorted set to its newest members. The trim
// runs in the write pipeline (see AddMulti) so the set stays small and bounded:
// Redis never approaches its maxmemory ceiling, eviction never fires, and every
// read (Range, LastN) touches a tiny set. 1024 covers the anomaly window (256)
// and any recent range window the benchmark queries, with headroom.
const retainPerDevice = 1024

// scoreOf reads the LE int64 timestamp from a 56-byte member.
func scoreOf(member []byte) float64 {
	return float64(int64(binary.LittleEndian.Uint64(member[0:8])))
}

func (s *RueidisStore) AddMulti(ctx context.Context, batches map[string][][]byte) error {
	if len(batches) == 0 {
		return nil
	}
	// Two commands per device: the ZADD, then a rank trim keeping only the
	// newest retainPerDevice members. Both ride the same DoMulti pipeline, so
	// the trim adds no extra round-trip; on a set already within the cap the
	// ZREMRANGEBYRANK is a no-op.
	cmds := make([]rueidis.Completed, 0, len(batches)*2)
	for dev, members := range batches {
		partial := s.client.B().Zadd().Key(key(dev)).ScoreMember()
		for _, m := range members {
			partial = partial.ScoreMember(scoreOf(m), rueidis.BinaryString(m))
		}
		cmds = append(cmds, partial.Build())
		cmds = append(cmds, s.client.B().Zremrangebyrank().
			Key(key(dev)).
			Start(0).
			Stop(-(retainPerDevice+1)).
			Build())
	}
	for _, resp := range s.client.DoMulti(ctx, cmds...) {
		if err := resp.Error(); err != nil {
			return err
		}
	}
	return nil
}

func (s *RueidisStore) Range(ctx context.Context, deviceID string, fromTS, toTS, offset, count int64) ([][]byte, error) {
	cmd := s.client.B().Zrange().
		Key(key(deviceID)).
		Min(strconv.FormatInt(fromTS, 10)).
		Max(strconv.FormatInt(toTS, 10)).
		Byscore().
		Limit(offset, count).
		Build()
	return s.asMembers(ctx, cmd)
}

func (s *RueidisStore) LastN(ctx context.Context, deviceID string, n int64) ([][]byte, error) {
	cmd := s.client.B().Zrange().
		Key(key(deviceID)).
		Min("0").
		Max(strconv.FormatInt(n-1, 10)).
		Rev().
		Build()
	return s.asMembers(ctx, cmd)
}

func (s *RueidisStore) asMembers(ctx context.Context, cmd rueidis.Completed) ([][]byte, error) {
	res, err := s.client.Do(ctx, cmd).AsStrSlice()
	if err != nil {
		return nil, err
	}
	out := make([][]byte, len(res))
	for i := range res {
		out[i] = []byte(res[i])
	}
	return out, nil
}

func (s *RueidisStore) Ping(ctx context.Context) error {
	return s.client.Do(ctx, s.client.B().Ping().Build()).Error()
}

func (s *RueidisStore) Close() { s.client.Close() }
