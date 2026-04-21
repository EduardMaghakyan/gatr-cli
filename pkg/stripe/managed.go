package stripe

import "strings"

// Metadata keys gatr writes onto every product / price it creates.
// Reads filter on these — anything missing the pair is operator-owned
// and left untouched.
const (
	metaKeyManaged = "gatr_managed"
	metaKeyGatrID  = "gatr_id"
)

// Stripe meters have no metadata field, so we identify gatr-managed
// meters by event_name prefix instead. Format:
//
//	gatr_<hex(project_uuid)>_<yaml_id>
//
// The hex form strips the UUID's hyphens to satisfy Stripe's
// event_name regex ([a-zA-Z][a-zA-Z0-9_]*, max 100 chars). With a
// 32-hex project segment + the "gatr_" + "_" overhead, yaml ids up to
// 62 chars fit — well above the typical 1-30 char range.
const meterEventNamePrefix = "gatr_"

// gatrIDFor renders the namespaced metadata value for a (project, yaml)
// pair. Stable across CLI invocations so reruns find their own objects.
func gatrIDFor(projectID, yamlID string) string {
	return projectID + ":" + yamlID
}

// parseGatrID returns the yaml id half of a "<project>:<yaml>" value
// IFF the project matches the supplied projectID. ok=false means the
// value belongs to a different gatr project on the same Stripe account
// — we leave those objects strictly alone.
func parseGatrID(value, projectID string) (yamlID string, ok bool) {
	prefix := projectID + ":"
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	return value[len(prefix):], true
}

// isGatrManaged is the read-side gate: gatr_managed=="true" AND
// gatr_id namespaced to the supplied project. Both must hold.
func isGatrManaged(meta map[string]string, projectID string) (yamlID string, ok bool) {
	if meta[metaKeyManaged] != "true" {
		return "", false
	}
	return parseGatrID(meta[metaKeyGatrID], projectID)
}

// hexProjectID strips hyphens from a UUID for use in meter event_names,
// which are restricted to [a-zA-Z][a-zA-Z0-9_]*. Non-hex input passes
// through unchanged — the Stripe API will reject malformed names at
// create time, which is the correct surface for that error.
func hexProjectID(projectID string) string {
	return strings.ReplaceAll(projectID, "-", "")
}

// meterEventNameFor produces the Stripe meter event_name for a yaml id
// scoped to a project. Symmetric with parseMeterEventName.
func meterEventNameFor(projectID, yamlID string) string {
	return meterEventNamePrefix + hexProjectID(projectID) + "_" + yamlID
}

// parseMeterEventName is the inverse of meterEventNameFor: returns the
// yaml id half if event_name belongs to this project, else ok=false.
func parseMeterEventName(eventName, projectID string) (yamlID string, ok bool) {
	prefix := meterEventNamePrefix + hexProjectID(projectID) + "_"
	if !strings.HasPrefix(eventName, prefix) {
		return "", false
	}
	return eventName[len(prefix):], true
}

// ManagedProduct is a Stripe product augmented with the resolved gatr
// yaml id. Only fields needed by the diff engine are projected — full
// stripe-go Product objects carry far more than gatr cares about.
type ManagedProduct struct {
	StripeID    string
	YamlID      string
	Name        string
	Description string
	Active      bool
	Metadata    map[string]string
}

// ManagedPrice is a Stripe price (recurring or one-time) augmented
// with the resolved gatr yaml id. Recurring is nil for one-time prices.
type ManagedPrice struct {
	StripeID        string
	YamlID          string
	ProductStripeID string
	UnitAmount      int64
	Currency        string
	Recurring       *RecurringInfo
	Active          bool
	Metadata        map[string]string
}

// RecurringInfo captures the subset of stripe.PriceRecurring the diff
// engine needs. UsageType is "licensed" (per-seat) or "metered". MeterID
// is set only when UsageType=="metered".
type RecurringInfo struct {
	Interval      string
	IntervalCount int64
	UsageType     string
	MeterID       string
}

// ManagedMeter is a Stripe billing meter scoped to this project via
// event_name prefix. Aggregation is the formula string from
// DefaultAggregation (e.g. "sum", "count", "last").
type ManagedMeter struct {
	StripeID    string
	YamlID      string
	DisplayName string
	EventName   string
	Aggregation string
	Status      string
}
