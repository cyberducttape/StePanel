package main

import (
	"strings"
	"syscall"
	"testing"
)

// F-02: the root broker socket admits only the exact StePanel service
// identity, not any process whose primary group happens to match.
func TestBrokerSocketRequiresExactServiceIdentity(t *testing.T) {
	allowed := peerIdentity{uid: 991, gid: 991}
	if err := authorizePeerCredential(&syscall.Ucred{Pid: 10, Uid: 991, Gid: 991}, allowed); err != nil {
		t.Fatalf("service identity refused: %v", err)
	}
	for name, peer := range map[string]*syscall.Ucred{
		"same group, other user": {Pid: 11, Uid: 1500, Gid: 991},
		"same user, other group": {Pid: 12, Uid: 991, Gid: 100},
		"root":                   {Pid: 13, Uid: 0, Gid: 0},
		"root in the group":      {Pid: 14, Uid: 0, Gid: 991},
	} {
		err := authorizePeerCredential(peer, allowed)
		if err == nil {
			t.Fatalf("%s: peer was admitted", name)
		}
		if !strings.Contains(err.Error(), "security: unauthorized peer") || !strings.Contains(err.Error(), "pid=") {
			t.Fatalf("%s: refusal does not record the peer credentials: %v", name, err)
		}
	}
	if err := authorizePeerCredential(nil, allowed); err == nil {
		t.Fatal("missing peer credentials were admitted")
	}
}
