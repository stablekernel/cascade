package harness

import (
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"strconv"
)

// Environment variables that select a scenario shard for one CI matrix leg.
const (
	envShardIndex = "E2E_SHARD_INDEX"
	envShardTotal = "E2E_SHARD_TOTAL"
)

// ShardConfig partitions the e2e scenario set across parallel CI runners. The
// default value (Index 0, Total 1) selects the whole set, so a plain local
// `go test` run is unaffected and every selection becomes a no-op.
type ShardConfig struct {
	// Index is the zero-based position of this shard within the matrix.
	Index int
	// Total is the number of shards the suite is split across.
	Total int
}

// ShardFromEnv builds a ShardConfig from E2E_SHARD_INDEX and E2E_SHARD_TOTAL.
// Unset or blank variables fall back to the single-shard default (0 of 1). It
// returns an error when the values are non-numeric or out of range so a
// misconfigured matrix leg fails loudly instead of silently dropping scenarios.
func ShardFromEnv() (ShardConfig, error) {
	cfg := ShardConfig{Index: 0, Total: 1}

	total, ok, err := lookupShardInt(envShardTotal)
	if err != nil {
		return cfg, err
	}
	if ok {
		cfg.Total = total
	}

	index, ok, err := lookupShardInt(envShardIndex)
	if err != nil {
		return cfg, err
	}
	if ok {
		cfg.Index = index
	}

	if cfg.Total < 1 {
		return cfg, fmt.Errorf("%s must be >= 1, got %d", envShardTotal, cfg.Total)
	}
	if cfg.Index < 0 || cfg.Index >= cfg.Total {
		return cfg, fmt.Errorf("%s must be in [0, %d), got %d", envShardIndex, cfg.Total, cfg.Index)
	}
	return cfg, nil
}

// lookupShardInt reads an integer environment variable. The boolean reports
// whether the variable was set to a non-empty value.
func lookupShardInt(name string) (int, bool, error) {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return 0, false, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, fmt.Errorf("%s=%q is not an integer: %w", name, raw, err)
	}
	return v, true, nil
}

// SelectShard returns the items that belong to this shard. Items are first
// sorted by key for a stable, run-independent ordering, then distributed
// round-robin (position modulo Total) so heavy scenarios spread across shards
// instead of clustering in one contiguous block. The partition is by position,
// so the union of all shards equals the input and no item appears twice, even
// when keys collide. With Total <= 1 it returns the input unchanged.
func SelectShard[T any](items []T, key func(T) string, cfg ShardConfig) []T {
	if cfg.Total <= 1 {
		return items
	}

	sorted := make([]T, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		return key(sorted[i]) < key(sorted[j])
	})

	out := make([]T, 0, len(sorted)/cfg.Total+1)
	for i, item := range sorted {
		if i%cfg.Total == cfg.Index {
			out = append(out, item)
		}
	}
	return out
}

// Owns reports whether a uniquely named, standalone scenario belongs to this
// shard. Each name maps to exactly one shard through a stable hash, so across
// the full matrix every named scenario runs once and only once. With Total <= 1
// it always returns true.
func (c ShardConfig) Owns(name string) bool {
	if c.Total <= 1 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int(h.Sum32()%uint32(c.Total)) == c.Index
}
