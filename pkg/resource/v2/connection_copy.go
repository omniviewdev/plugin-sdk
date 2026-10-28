package resourcev2

import (
	"reflect"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// CloneConnection gives native utility callbacks independent ownership of the
// configured profile without coercing exact scalars or private credentials.
func CloneConnection(connection types.Connection) types.Connection {
	return cloneConnection(connection)
}

// SameConnectionClientConfig compares source-affecting endpoint/auth configuration.
// Labels and Lifecycle are IDE organization/orchestration, not client inputs;
// display fields, refresh/expiry bookkeeping and opaque runtime Client are not
// source identity. Native client builders must keep endpoint/auth in Data or
// private sensitive data. This does not establish native authority continuity.
func SameConnectionClientConfig(a, b types.Connection) bool {
	return a.ID == b.ID && a.UID == b.UID &&
		reflect.DeepEqual(a.Data, b.Data) &&
		reflect.DeepEqual(a.GetSensitiveData(), b.GetSensitiveData())
}

// Connection configuration is copied only when it crosses the controller's
// ownership boundary. Native, desired, effective, and client-slot inventories
// may share their immutable internal values; callers and callbacks never do.
func cloneConnection(connection types.Connection) types.Connection {
	var copy metadataCopy
	connection.Data = copy.value(reflect.ValueOf(connection.Data)).Interface().(map[string]any)
	connection.Labels = copy.value(reflect.ValueOf(connection.Labels)).Interface().(map[string]any)
	connection.SetSensitiveData(copy.value(reflect.ValueOf(connection.GetSensitiveData())).Interface().(map[string]any))
	connection.Lifecycle = copy.value(reflect.ValueOf(connection.Lifecycle)).Interface().(types.ConnectionLifecycle)
	// Client is a legacy shared-utility runtime handle, not configuration. Keep
	// opaque native handles intact, but own JSON containers stored in this field.
	if value := reflect.ValueOf(connection.Client); value.IsValid() {
		switch value.Kind() {
		case reflect.Map, reflect.Slice, reflect.Array:
			connection.Client = copy.value(value).Interface()
		}
	}
	return connection
}

type metadataCopyVisit struct {
	typ     reflect.Type
	pointer uintptr
	length  int
}
type metadataCopy struct {
	seen map[metadataCopyVisit]reflect.Value
}

func (c *metadataCopy) remember(v reflect.Value, length int, makeCopy func() reflect.Value) (reflect.Value, bool) {
	key := metadataCopyVisit{typ: v.Type(), pointer: uintptr(v.UnsafePointer()), length: length}
	if copied, ok := c.seen[key]; ok {
		return copied, true
	}
	copied := makeCopy()
	if c.seen == nil {
		c.seen = make(map[metadataCopyVisit]reflect.Value)
	}
	c.seen[key] = copied
	return copied, false
}

// Preserve concrete native scalar/container types and nils. In particular,
// int64/uint64/json.Number and signed double zero never pass through JSON.
// Memoization also preserves shared or cyclic private credential containers.
func (c *metadataCopy) value(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(c.value(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out, seen := c.remember(v, 0, func() reflect.Value { return reflect.MakeMapWithSize(v.Type(), v.Len()) })
		if seen {
			return out
		}
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(it.Key(), c.value(it.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out, seen := c.remember(v, v.Len(), func() reflect.Value { return reflect.MakeSlice(v.Type(), v.Len(), v.Len()) })
		if seen {
			return out
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			reflect.Copy(out, v)
			return out
		}
		for i := range v.Len() {
			out.Index(i).Set(c.value(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			out.Index(i).Set(c.value(v.Index(i)))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out, seen := c.remember(v, 0, func() reflect.Value { return reflect.New(v.Type().Elem()) })
		if !seen {
			out.Elem().Set(c.value(v.Elem()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if field.PkgPath == "" && field.Tag.Get("json") != "-" {
				out.Field(i).Set(c.value(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}
