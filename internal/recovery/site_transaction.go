package recovery

import "fmt"

// SiteTransactionState is the persisted phase of the filesystem portion of a
// site lifecycle transaction. Database and service convergence are tracked by
// their own journals and must not be inferred from this state.
type SiteTransactionState string

const (
	SiteTransactionPrepared    SiteTransactionState = "prepared"
	SiteTransactionBackedUp    SiteTransactionState = "site-backed-up"
	SiteTransactionRollingBack SiteTransactionState = "rolling-back"
	SiteTransactionCommitted   SiteTransactionState = "committed"
	SiteTransactionRolledBack  SiteTransactionState = "rolled-back"
)

// ValidateSiteTransactionState rejects unknown persisted states rather than
// letting recovery guess whether an unfamiliar phase is safe to roll back.
func ValidateSiteTransactionState(state SiteTransactionState) error {
	switch state {
	case SiteTransactionPrepared, SiteTransactionBackedUp,
		SiteTransactionRollingBack, SiteTransactionCommitted,
		SiteTransactionRolledBack:
		return nil
	default:
		return fmt.Errorf("unknown site transaction state %q", state)
	}
}

// ValidateSiteTransactionTransition keeps the on-disk filesystem journal in
// its documented order. Repeating rolling-back and rolled-back transitions is
// allowed so interrupted compensation can be safely resumed.
func ValidateSiteTransactionTransition(current, next SiteTransactionState) error {
	if err := ValidateSiteTransactionState(current); err != nil {
		return err
	}
	if err := ValidateSiteTransactionState(next); err != nil {
		return err
	}
	valid := false
	switch current {
	case SiteTransactionPrepared:
		valid = next == SiteTransactionBackedUp || next == SiteTransactionRollingBack ||
			next == SiteTransactionCommitted || next == SiteTransactionRolledBack
	case SiteTransactionBackedUp:
		valid = next == SiteTransactionRollingBack || next == SiteTransactionCommitted
	case SiteTransactionRollingBack:
		valid = next == SiteTransactionRollingBack || next == SiteTransactionRolledBack
	case SiteTransactionRolledBack:
		valid = next == SiteTransactionRolledBack
	case SiteTransactionCommitted:
		valid = next == SiteTransactionCommitted
	}
	if !valid {
		return fmt.Errorf("invalid site transaction transition %q -> %q", current, next)
	}
	return nil
}
