package operations

import (
	"context"
	"errors"
	"testing"
)

func TestFencingTokensRoundTripAndInvalidTokenFailsClosed(t *testing.T) {
	if got := FencingTokens(nil); got != nil {
		t.Fatalf("nil context tokens = %#v", got)
	}
	base := context.Background()
	first := FencingToken{ResourceKey: "site:a", OwnerID: "one", Generation: 1}
	second := FencingToken{ResourceKey: "site:b", OwnerID: "two", Generation: 2}
	withFirst := WithFencingTokens(base, first)
	withBoth := WithFencingTokens(withFirst, second)
	got := FencingTokens(withBoth)
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("fencing tokens = %#v", got)
	}
	got[0].OwnerID = "mutated"
	if FencingTokens(withBoth)[0].OwnerID != first.OwnerID {
		t.Fatal("FencingTokens returned mutable context storage")
	}
	if got := WithFencingTokens(base); got != base {
		t.Fatal("empty token set should preserve context")
	}
	if err := VerifyFencingToken(nil, first); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("nil database token verification = %v", err)
	}
	if err := VerifyFencingToken(nil, FencingToken{}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("invalid token verification = %v", err)
	}
}
