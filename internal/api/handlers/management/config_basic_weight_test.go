package management

import "testing"

func TestNormalizeRoutingStrategyWeightedRoundRobin(t *testing.T) {
	for _, input := range []string{"weighted-round-robin", "weightedroundrobin", "wrr"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "weighted-round-robin" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want weighted-round-robin, true", input, got, ok)
		}
	}
}

func TestNormalizeRoutingStrategySeqRandom(t *testing.T) {
	for _, input := range []string{"seq-random", "sequential-random", "seqrandom", "sr"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "seq-random" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want seq-random, true", input, got, ok)
		}
	}
}
