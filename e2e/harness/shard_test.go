package harness

import (
	"fmt"
	"os"
	"sort"
	"testing"
)

func TestShardFromEnv(t *testing.T) {
	tests := []struct {
		name      string
		index     string // "" means leave the variable unset
		total     string
		want      ShardConfig
		wantError bool
	}{
		{name: "unset defaults to whole suite", want: ShardConfig{Index: 0, Total: 1}},
		{name: "valid mid shard", index: "2", total: "5", want: ShardConfig{Index: 2, Total: 5}},
		{name: "valid last shard", index: "4", total: "5", want: ShardConfig{Index: 4, Total: 5}},
		{name: "total without index defaults index zero", total: "3", want: ShardConfig{Index: 0, Total: 3}},
		{name: "non-numeric total", total: "five", wantError: true},
		{name: "non-numeric index", index: "x", total: "5", wantError: true},
		{name: "zero total", total: "0", wantError: true},
		{name: "negative index", index: "-1", total: "5", wantError: true},
		{name: "index equal to total", index: "5", total: "5", wantError: true},
		{name: "index above total", index: "9", total: "5", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOrUnset(t, envShardIndex, tt.index)
			setOrUnset(t, envShardTotal, tt.total)

			got, err := ShardFromEnv()
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got config %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ShardFromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// setOrUnset sets an environment variable for the duration of the test, or
// clears it when value is empty so the default (unset) path is exercised. The
// prior value is restored on cleanup so cases do not leak into one another.
func setOrUnset(t *testing.T, name, value string) {
	t.Helper()
	prev, had := os.LookupEnv(name)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, prev)
			return
		}
		_ = os.Unsetenv(name)
	})
	if value == "" {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.Setenv(name, value); err != nil {
		t.Fatal(err)
	}
}

func TestSelectShard_TotalOneIsNoOp(t *testing.T) {
	items := makeKeyed(10)
	got := SelectShard(items, keyOf, ShardConfig{Index: 0, Total: 1})
	if len(got) != len(items) {
		t.Fatalf("Total=1 changed the set: got %d, want %d", len(got), len(items))
	}
	for i := range items {
		if got[i] != items[i] {
			t.Fatalf("Total=1 reordered items at %d", i)
		}
	}
}

func TestSelectShard_Deterministic(t *testing.T) {
	items := makeKeyed(37)
	cfg := ShardConfig{Index: 1, Total: 4}
	first := SelectShard(items, keyOf, cfg)
	// Re-run against a shuffled copy: a stable sort by key must yield the same
	// selection regardless of input order.
	shuffled := make([]keyed, len(items))
	copy(shuffled, items)
	sort.SliceStable(shuffled, func(i, j int) bool { return shuffled[i].name > shuffled[j].name })
	second := SelectShard(shuffled, keyOf, cfg)

	if len(first) != len(second) {
		t.Fatalf("non-deterministic length: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("non-deterministic selection at %d: %v vs %v", i, first[i], second[i])
		}
	}
}

func TestSelectShard_PartitionIsCompleteBalancedAndDisjoint(t *testing.T) {
	for _, total := range []int{2, 3, 5, 8} {
		for _, count := range []int{0, 1, 7, 76} {
			t.Run(fmt.Sprintf("total=%d/count=%d", total, count), func(t *testing.T) {
				items := makeKeyed(count)

				seen := make(map[string]int, count)
				sizes := make([]int, total)
				for idx := 0; idx < total; idx++ {
					shard := SelectShard(items, keyOf, ShardConfig{Index: idx, Total: total})
					sizes[idx] = len(shard)
					for _, it := range shard {
						seen[it.name]++
					}
				}

				// Complete: every item selected exactly once (union, no overlap).
				if len(seen) != count {
					t.Fatalf("union covered %d items, want %d", len(seen), count)
				}
				for name, n := range seen {
					if n != 1 {
						t.Fatalf("item %q appeared in %d shards, want 1", name, n)
					}
				}

				// Balanced: shard sizes differ by at most one.
				minSize, maxSize := sizes[0], sizes[0]
				for _, s := range sizes {
					if s < minSize {
						minSize = s
					}
					if s > maxSize {
						maxSize = s
					}
				}
				if maxSize-minSize > 1 {
					t.Fatalf("unbalanced shards %v (spread %d)", sizes, maxSize-minSize)
				}
			})
		}
	}
}

func TestShardConfig_Owns(t *testing.T) {
	const total = 5
	names := []string{
		"TestInitScaffoldOrchestratesAndPromotes",
		"TestMultiRepoScenario_SatelliteNotifiesPrimary",
		"TestMultiRepoScenario_ExternalStatePromotion",
		"TestMultiRepoScenario_MixedDeploys",
	}

	// Total=1 owns everything.
	whole := ShardConfig{Index: 0, Total: 1}
	for _, n := range names {
		if !whole.Owns(n) {
			t.Fatalf("Total=1 should own %q", n)
		}
	}

	// Across the matrix each name is owned by exactly one shard.
	for _, n := range names {
		owners := 0
		for idx := 0; idx < total; idx++ {
			if (ShardConfig{Index: idx, Total: total}).Owns(n) {
				owners++
			}
		}
		if owners != 1 {
			t.Fatalf("name %q owned by %d shards, want 1", n, owners)
		}
	}
}

type keyed struct {
	name string
}

func keyOf(k keyed) string { return k.name }

func makeKeyed(n int) []keyed {
	items := make([]keyed, n)
	for i := 0; i < n; i++ {
		// Zero-padded so lexical order is independent of width.
		items[i] = keyed{name: fmt.Sprintf("scenario-%04d", i)}
	}
	return items
}
