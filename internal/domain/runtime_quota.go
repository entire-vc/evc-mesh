package domain

import (
	"math"
	"regexp"
	"slices"
	"time"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

const (
	RuntimeQuotaSchemaVersion   = 1
	runtimeQuotaMaxObservations = 256
	runtimeQuotaMaxAgeCeiling   = 30 * 24 * 3600
)

var (
	runtimeQuotaTokenPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	runtimeQuotaStatuses     = []string{"available", "exhausted", "unknown", "auth_error", "network_error"}
	runtimeQuotaConfidences  = []string{"high", "medium", "low", "none"}
)

// RuntimeQuotaObservation is one provider window of one catalog pool as the
// controller saw it. Nullable measurements stay nil when the provider gave no
// number: "no data" is stored and returned as null, never as 0 %.
type RuntimeQuotaObservation struct {
	Pool          string     `json:"pool"`
	Provider      string     `json:"provider"`
	Window        string     `json:"window"`
	Unit          string     `json:"unit"`
	UsedFraction  *float64   `json:"used_fraction"`
	Remaining     *float64   `json:"remaining"`
	ResetAt       *time.Time `json:"reset_at"`
	ObservedAt    time.Time  `json:"observed_at"`
	Source        string     `json:"source"`
	Confidence    string     `json:"confidence"`
	MaxAgeSeconds int        `json:"max_age_seconds"`
	Status        string     `json:"status"`
	ErrorKind     *string    `json:"error_kind"`
}

type RuntimeQuotaReport struct {
	SchemaVersion int                       `json:"schema_version"`
	Observations  []RuntimeQuotaObservation `json:"observations"`
}

// RuntimeQuotaView is what GET returns: the stored observation plus a stale
// flag computed at read time from max_age_seconds.
type RuntimeQuotaView struct {
	RuntimeQuotaObservation
	ReceivedAt time.Time `json:"received_at"`
	Stale      bool      `json:"stale"`
}

type RuntimeQuotaList struct {
	ControllerRef string             `json:"controller_ref"`
	Observations  []RuntimeQuotaView `json:"observations"`
}

// Stale reports whether the observation is older than its own max age at now.
func (o RuntimeQuotaObservation) Stale(now time.Time) bool {
	return now.Sub(o.ObservedAt) > time.Duration(o.MaxAgeSeconds)*time.Second
}

// Validate checks one observation in isolation. Pool ownership and provider
// match need the catalog and are checked by the repository.
func (o RuntimeQuotaObservation) Validate(now time.Time) error {
	if !ValidRuntimeRef(o.Pool) || !ValidRuntimeRef(o.Provider) {
		return apierror.BadRequest("invalid quota observation")
	}
	if !runtimeQuotaTokenPattern.MatchString(o.Window) || !runtimeQuotaTokenPattern.MatchString(o.Unit) || !runtimeQuotaTokenPattern.MatchString(o.Source) {
		return apierror.BadRequest("invalid quota observation")
	}
	if !slices.Contains(runtimeQuotaStatuses, o.Status) || !slices.Contains(runtimeQuotaConfidences, o.Confidence) {
		return apierror.BadRequest("invalid quota observation")
	}
	if o.UsedFraction != nil && (math.IsNaN(*o.UsedFraction) || *o.UsedFraction < 0 || *o.UsedFraction > 1) {
		return apierror.BadRequest("used_fraction must be within 0..1 or null")
	}
	if o.Remaining != nil && (math.IsNaN(*o.Remaining) || math.IsInf(*o.Remaining, 0) || *o.Remaining < 0) {
		return apierror.BadRequest("remaining must be a non-negative number or null")
	}
	if o.ObservedAt.IsZero() || o.ObservedAt.After(now) {
		return apierror.BadRequest("observed_at must be set and not in the future")
	}
	if o.MaxAgeSeconds < 1 || o.MaxAgeSeconds > runtimeQuotaMaxAgeCeiling {
		return apierror.BadRequest("max_age_seconds out of range")
	}
	if o.ErrorKind != nil && !runtimeQuotaTokenPattern.MatchString(*o.ErrorKind) {
		return apierror.BadRequest("invalid error_kind")
	}
	// A status that claims a measurement needs one; "unknown" and error
	// statuses must not carry numbers they cannot have.
	if o.Status != "available" && o.Status != "exhausted" && (o.UsedFraction != nil || o.Remaining != nil) {
		return apierror.BadRequest("a status without data must not carry measurements")
	}
	return nil
}

// Validate checks the envelope and every observation; no partial acceptance.
func (r RuntimeQuotaReport) Validate(now time.Time) error {
	if r.SchemaVersion != RuntimeQuotaSchemaVersion || len(r.Observations) == 0 || len(r.Observations) > runtimeQuotaMaxObservations {
		return apierror.BadRequest("invalid quota report")
	}
	for _, o := range r.Observations {
		if err := o.Validate(now); err != nil {
			return err
		}
	}
	return nil
}
