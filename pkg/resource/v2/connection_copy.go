package resourcev2

import (
	"reflect"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// CloneConnection copies mutable configured-profile containers without JSON
// roundtripping or scalar coercion. It preserves nils and concrete scalar types
// and copies exported credential fields even when tagged json:"-".
// Unexported implementation state and opaque runtime handles retain native
// ownership; this is not a general-purpose native-client clone. Map/slice/array
// containers in Client are copied. Do not mutate input concurrently with copying.
// Cloning neither validates configuration nor applies it to a registry.
func CloneConnection(connection types.Connection) types.Connection {
	return cloneConnection(connection)
}

// SameConnectionClientConfig compares source-affecting endpoint/auth configuration.
// Labels and Lifecycle are IDE organization/orchestration, not client inputs;
// display fields, refresh/expiry bookkeeping and opaque runtime Client are not
// source identity. Native client builders must keep endpoint/auth in Data or
// private sensitive data. This does not establish native authority continuity.
// Nil and empty configuration containers are distinct under this comparison.
// Keep both inputs stable during comparison. Equality only decides client reuse;
// it neither compares current network state nor authorizes resource access.
func SameConnectionClientConfig(a, b types.Connection) bool {
	return a.ID == b.ID && a.UID == b.UID &&
		reflect.DeepEqual(a.Data, b.Data) &&
		reflect.DeepEqual(a.GetSensitiveData(), b.GetSensitiveData())
}

// Connection configuration is copied only when it crosses the controller's
// ownership boundary. Native, desired, effective, and client-slot inventories
// may share their immutable internal values; callers and callbacks never do.
func cloneConnection(connection types.Connection) types.Connection {
	var copier metadataCopy
	connection.Data = copyMetadata(&copier, connection.Data)
	connection.Labels = copyMetadata(&copier, connection.Labels)
	connection.SetSensitiveData(copyMetadata(&copier, connection.GetSensitiveData()))
	connection.Lifecycle = copyMetadata(&copier, connection.Lifecycle)
	// Client is a legacy shared-utility runtime handle, not configuration. Keep
	// opaque native handles intact, but own JSON containers stored in this field.
	if value := reflect.ValueOf(connection.Client); value.IsValid() {
		kind := value.Kind()
		if kind == reflect.Map || kind == reflect.Slice || kind == reflect.Array {
			connection.Client = copier.value(value).Interface()
		}
	}
	return connection
}

func copyMetadata[T any](copier *metadataCopy, value T) T {
	cloned, ok := reflect.TypeAssert[T](copier.value(reflect.ValueOf(value)))
	if !ok {
		panic("connection metadata copy changed its concrete type")
	}
	return cloned
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
		return c.mapValue(v)
	case reflect.Slice:
		return c.sliceValue(v)
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		c.elements(out, v)
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
		return c.structValue(v)
	case reflect.Invalid, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128,
		reflect.Chan, reflect.Func, reflect.String, reflect.UnsafePointer:
		return v
	}
	return v
}

func (c *metadataCopy) mapValue(v reflect.Value) reflect.Value {
	if v.IsNil() {
		return v
	}
	out, seen := c.remember(v, 0, func() reflect.Value { return reflect.MakeMapWithSize(v.Type(), v.Len()) })
	if !seen {
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(it.Key(), c.value(it.Value()))
		}
	}
	return out
}

func (c *metadataCopy) sliceValue(v reflect.Value) reflect.Value {
	if v.IsNil() {
		return v
	}
	out, seen := c.remember(v, v.Len(), func() reflect.Value {
		return reflect.MakeSlice(v.Type(), v.Len(), v.Len())
	})
	if !seen {
		if v.Type().Elem().Kind() == reflect.Uint8 {
			reflect.Copy(out, v)
		} else {
			c.elements(out, v)
		}
	}
	return out
}

func (c *metadataCopy) elements(out, v reflect.Value) {
	for i := range v.Len() {
		out.Index(i).Set(c.value(v.Index(i)))
	}
}

func (c *metadataCopy) structValue(v reflect.Value) reflect.Value {
	out := reflect.New(v.Type()).Elem()
	out.Set(v)
	for i := range v.NumField() {
		// Serialization visibility does not determine configuration ownership.
		// Unexported implementation state remains opaque and shallow-copied.
		if v.Type().Field(i).PkgPath == "" {
			out.Field(i).Set(c.value(v.Field(i)))
		}
	}
	return out
}
