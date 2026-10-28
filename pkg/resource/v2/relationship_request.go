package resourcev2

// RelationshipResolutionRequest supplies explicit host-admitted target bindings
// for observed native links. Bindings are not permission grants and never
// authorize enumeration or scans. The provider must not reuse source authority
// for a different target or fabricate a missing binding; unsupported/unresolved
// links fail explicitly rather than silently becoming complete empty results.
// Target bindings must have explicit scopes and must not overlap for the same
// provider, type, and route. Such bindings must share query dimension sets.
// Returned references must preserve binding authority and cover every binding
// query dimension; additional identity dimensions remain part of the reference.
// An empty target list admits no links. JSON admission rejects unknown or
// duplicate members and null arrays.
type RelationshipResolutionRequest struct {
	Source  GetRequest   `json:"source"`
	Targets []ReadTarget `json:"targets"`
}

// MaxRelationshipTargets is the bounded implementation-candidate ceiling for
// explicit relationship bindings, independent of inventory cardinality.
const MaxRelationshipTargets = 256
