// Package wirecost counts protobuf message and field occurrences.
package wirecost

import "google.golang.org/protobuf/reflect/protoreflect"

// Count returns encoded message and field occurrences, including the root.
func Count(m protoreflect.Message) (nodes, fields int) {
	nodes = 1
	m.Range(func(fd protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if fd.IsMap() {
			value.Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
				fields += 3
				nodes++
				if fd.MapValue().Message() != nil {
					n, f := Count(v.Message())
					nodes += n
					fields += f
				}
				return true
			})
		} else if fd.IsList() {
			list := value.List()
			if fd.IsPacked() {
				fields++
			} else {
				fields += list.Len()
			}
			if fd.Message() != nil {
				for i := range list.Len() {
					n, f := Count(list.Get(i).Message())
					nodes += n
					fields += f
				}
			}
		} else {
			fields++
			if fd.Message() != nil {
				n, f := Count(value.Message())
				nodes += n
				fields += f
			}
		}
		return true
	})
	return nodes, fields
}
