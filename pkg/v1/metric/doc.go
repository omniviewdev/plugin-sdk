// Package metric provides metric queries and subscription streams.
//
// Resource inputs use the canonical resource/v2 Value without Struct conversion.
// ResourceData, Reference, or Route selects resource intent; opaque ResourceKey
// selectors and Params alone do not. Query automatically selects QueryResource
// for resource intent and never retries the legacy operation.
//
// StreamMetrics accepts only nonresource frames. StreamResourceMetrics is an
// explicit mixed resource/nonresource lane: its wire-v2 opening handshake must
// succeed before a client publishes the stream. Every subsequent subscription
// and control frame uses wire version two. Resource references and access routes
// remain optional actual inputs, never synthesized from ordinary selectors.
//
// QueryContext.Context and StreamContext.Context carry callback cancellation.
// Polling retains the complete resource input and Params for every query.
// StreamOutput.OperationError carries typed admission and transport failures;
// Error remains the native provider diagnostic field.
package metric
