package recovery

import "testing"

func TestSiteTransactionTransitions(t *testing.T) {
	valid := [][2]SiteTransactionState{
		{SiteTransactionPrepared, SiteTransactionBackedUp},
		{SiteTransactionPrepared, SiteTransactionRollingBack},
		{SiteTransactionPrepared, SiteTransactionCommitted},
		{SiteTransactionPrepared, SiteTransactionRolledBack},
		{SiteTransactionBackedUp, SiteTransactionRollingBack},
		{SiteTransactionBackedUp, SiteTransactionCommitted},
		{SiteTransactionRollingBack, SiteTransactionRollingBack},
		{SiteTransactionRollingBack, SiteTransactionRolledBack},
		{SiteTransactionRolledBack, SiteTransactionRolledBack},
		{SiteTransactionCommitted, SiteTransactionCommitted},
	}
	for _, transition := range valid {
		if err := ValidateSiteTransactionTransition(transition[0], transition[1]); err != nil {
			t.Errorf("transition %q -> %q rejected: %v", transition[0], transition[1], err)
		}
	}

	invalid := [][2]SiteTransactionState{
		{SiteTransactionBackedUp, SiteTransactionPrepared},
		{SiteTransactionRollingBack, SiteTransactionCommitted},
		{SiteTransactionCommitted, SiteTransactionRollingBack},
		{SiteTransactionRolledBack, SiteTransactionCommitted},
		{"future-state", SiteTransactionCommitted},
		{SiteTransactionPrepared, "future-state"},
	}
	for _, transition := range invalid {
		if err := ValidateSiteTransactionTransition(transition[0], transition[1]); err == nil {
			t.Errorf("invalid transition %q -> %q accepted", transition[0], transition[1])
		}
	}
}
