package main

import "testing"

func TestValidateRedisAllocation(t *testing.T) {
	valid := RedisAllocation{Site: "example", Database: 3, Namespace: "example:", MemoryMB: 64, Eviction: "allkeys-lru"}
	if err := validateRedisAllocation(valid, "example"); err != nil {
		t.Fatalf("validateRedisAllocation rejected a valid allocation: %v", err)
	}

	cases := []struct {
		name  string
		alloc RedisAllocation
		site  string
	}{
		{"site mismatch", valid, "other"},
		{"invalid site name", RedisAllocation{Site: "../etc", Namespace: "ns", MemoryMB: 64, Eviction: "allkeys-lru"}, "../etc"},
		{"negative database", RedisAllocation{Site: "example", Database: -1, Namespace: "ns", MemoryMB: 64, Eviction: "allkeys-lru"}, "example"},
		{"database too high", RedisAllocation{Site: "example", Database: 16, Namespace: "ns", MemoryMB: 64, Eviction: "allkeys-lru"}, "example"},
		{"memory too low", RedisAllocation{Site: "example", Namespace: "ns", MemoryMB: 8, Eviction: "allkeys-lru"}, "example"},
		{"memory too high", RedisAllocation{Site: "example", Namespace: "ns", MemoryMB: 2000000, Eviction: "allkeys-lru"}, "example"},
		{"empty namespace", RedisAllocation{Site: "example", Namespace: "", MemoryMB: 64, Eviction: "allkeys-lru"}, "example"},
		{"unsupported eviction policy", RedisAllocation{Site: "example", Namespace: "ns", MemoryMB: 64, Eviction: "volatile-lru"}, "example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateRedisAllocation(c.alloc, c.site); err == nil {
				t.Fatalf("validateRedisAllocation accepted an invalid allocation: %#v", c.alloc)
			}
		})
	}
}
