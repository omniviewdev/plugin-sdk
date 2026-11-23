package execv2_test

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// This file guards proto/exec/v2/exec.proto at the level of its compiled descriptor, and needs nothing but the
// generated package: it compiles and runs even when the Go code that implements the contract does not.
//
//   - TestContractMatchesItsReviewedGolden freezes the whole contract as a readable listing.
//   - TestAbsenceIsUnknown enforces the one rule of the file header.
//   - The remaining tests are shape tests: each pins one decision of the contract review by name.

var update = flag.Bool("update", false,
	"rewrite exec.contract.golden from the compiled descriptor (only legitimate while the contract is unreleased)")

const (
	goldenPath    = "exec.contract.golden"
	goldenVersion = "contract_version "
)

var file = execv2.File_proto_exec_v2_exec_proto

// ---------------------------------------------------------------------------------------------------------------
// The reviewed golden
// ---------------------------------------------------------------------------------------------------------------

// relName is a full name without the package of the file, so that the listing reads well. A name of another package
// keeps its package.
func relName(n protoreflect.FullName) string {
	return strings.TrimPrefix(string(n), string(file.Package())+".")
}

func typeText(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return "message " + relName(fd.Message().FullName())
	case protoreflect.EnumKind:
		return "enum " + relName(fd.Enum().FullName())
	default:
		return fd.Kind().String()
	}
}

// packedText is "packed " or "unpacked " for a repeated field that can be packed (a number, a bool or an enum), and
// empty for one that cannot (a string, bytes, a message). It is the effective state, with the default of the syntax
// and any [packed] option applied: the two encodings are different bytes on the wire.
func packedText(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.MessageKind, protoreflect.GroupKind:
		return ""
	}
	if fd.IsPacked() {
		return "packed "
	}
	return "unpacked "
}

func fieldText(fd protoreflect.FieldDescriptor) string {
	switch {
	case fd.IsMap():
		return fmt.Sprintf("map<%s, %s>", typeText(fd.MapKey()), typeText(fd.MapValue()))
	case fd.Cardinality() == protoreflect.Repeated:
		return "repeated " + packedText(fd) + typeText(fd)
	case fd.HasOptionalKeyword():
		return "optional " + typeText(fd)
	}
	text := typeText(fd)
	if oneof := fd.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
		text += " oneof " + string(oneof.Name())
	}
	return text
}

// allMessages lists every message of the file, nested ones included, without map entries, sorted by name.
func allMessages() []protoreflect.MessageDescriptor {
	var out []protoreflect.MessageDescriptor
	var walk func(ms protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := range ms.Len() {
			m := ms.Get(i)
			if m.IsMapEntry() {
				continue
			}
			out = append(out, m)
			walk(m.Messages())
		}
	}
	walk(file.Messages())
	sort.Slice(out, func(i, j int) bool { return out[i].FullName() < out[j].FullName() })
	return out
}

// allEnums lists every enum of the file, nested ones included, sorted by name.
func allEnums() []protoreflect.EnumDescriptor {
	var out []protoreflect.EnumDescriptor
	for i := range file.Enums().Len() {
		out = append(out, file.Enums().Get(i))
	}
	for _, m := range allMessages() {
		for i := range m.Enums().Len() {
			out = append(out, m.Enums().Get(i))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullName() < out[j].FullName() })
	return out
}

func fieldsByNumber(m protoreflect.MessageDescriptor) []protoreflect.FieldDescriptor {
	out := make([]protoreflect.FieldDescriptor, 0, m.Fields().Len())
	for i := range m.Fields().Len() {
		out = append(out, m.Fields().Get(i))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number() < out[j].Number() })
	return out
}

// reservedText lists reserved numbers (inclusive ranges) and names.
func reservedText(ranges [][2]int64, names protoreflect.Names) []string {
	var out []string
	for _, r := range ranges {
		if r[0] == r[1] {
			out = append(out, fmt.Sprintf("  reserved %d", r[0]))
		} else {
			out = append(out, fmt.Sprintf("  reserved %d to %d", r[0], r[1]))
		}
	}
	for i := range names.Len() {
		out = append(out, fmt.Sprintf("  reserved %q", names.Get(i)))
	}
	return out
}

func fieldRanges(rs protoreflect.FieldRanges) [][2]int64 {
	var out [][2]int64
	for i := range rs.Len() {
		r := rs.Get(i)
		out = append(out, [2]int64{int64(r[0]), int64(r[1]) - 1}) // the end of a field range is exclusive
	}
	return out
}

func enumRanges(rs protoreflect.EnumRanges) [][2]int64 {
	var out [][2]int64
	for i := range rs.Len() {
		r := rs.Get(i)
		out = append(out, [2]int64{int64(r[0]), int64(r[1])}) // the end of an enum range is inclusive
	}
	return out
}

// listing is the canonical, sorted text of the contract in the compiled descriptor: the package, the services with
// their streaming flags, every enum with its numbers and every message with its fields (number, name, type, and
// whether the field is optional, repeated (packed or unpacked, where it can be either), a map or a member of a oneof). Comments, field order in the file and
// formatting do not appear; everything that is on the wire does.
func listing(version uint32) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%d\n", goldenVersion, version)
	fmt.Fprintf(&b, "syntax %s\n", file.Syntax())
	fmt.Fprintf(&b, "package %s\n", file.Package())

	services := file.Services()
	for i := range services.Len() {
		s := services.Get(i)
		fmt.Fprintf(&b, "\nservice %s\n", s.Name())
		methods := make([]protoreflect.MethodDescriptor, 0, s.Methods().Len())
		for j := range s.Methods().Len() {
			methods = append(methods, s.Methods().Get(j))
		}
		sort.Slice(methods, func(a, c int) bool { return methods[a].Name() < methods[c].Name() })
		for _, m := range methods {
			in, out := relName(m.Input().FullName()), relName(m.Output().FullName())
			if m.IsStreamingClient() {
				in = "stream " + in
			}
			if m.IsStreamingServer() {
				out = "stream " + out
			}
			fmt.Fprintf(&b, "  rpc %s(%s) returns (%s)\n", m.Name(), in, out)
		}
	}

	for _, e := range allEnums() {
		fmt.Fprintf(&b, "\nenum %s\n", relName(e.FullName()))
		values := make([]protoreflect.EnumValueDescriptor, 0, e.Values().Len())
		for j := range e.Values().Len() {
			values = append(values, e.Values().Get(j))
		}
		sort.SliceStable(values, func(a, c int) bool { return values[a].Number() < values[c].Number() })
		for _, v := range values {
			fmt.Fprintf(&b, "  %d %s\n", v.Number(), v.Name())
		}
		for _, line := range reservedText(enumRanges(e.ReservedRanges()), e.ReservedNames()) {
			b.WriteString(line + "\n")
		}
	}

	for _, m := range allMessages() {
		fmt.Fprintf(&b, "\nmessage %s\n", relName(m.FullName()))
		for _, fd := range fieldsByNumber(m) {
			fmt.Fprintf(&b, "  %d %s %s\n", fd.Number(), fd.Name(), fieldText(fd))
		}
		for _, line := range reservedText(fieldRanges(m.ReservedRanges()), m.ReservedNames()) {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// goldenContractVersion reads the contract_version header of a golden; the header is the one place that names the
// version the golden freezes.
func goldenContractVersion(golden string) (uint32, error) {
	first, _, _ := strings.Cut(golden, "\n")
	first = strings.TrimSpace(first)
	rest, ok := strings.CutPrefix(first, goldenVersion)
	if !ok {
		return 0, fmt.Errorf("the first line is %q, want %q followed by the version", first, strings.TrimSpace(goldenVersion))
	}
	v, err := strconv.ParseUint(rest, 10, 32)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("the contract version in %q is not a positive integer", first)
	}
	return uint32(v), nil
}

// unifiedDiff is a line diff of two texts with three lines of context: "-" is the golden, "+" the compiled contract.
func unifiedDiff(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	n, m := len(a), len(b)
	lcs := make([]int32, (n+1)*(m+1))
	at := func(i, j int) *int32 { return &lcs[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				*at(i, j) = *at(i+1, j+1) + 1
			} else {
				*at(i, j) = max(*at(i+1, j), *at(i, j+1))
			}
		}
	}
	type op struct {
		tag  byte
		text string
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case *at(i+1, j) >= *at(i, j+1):
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j]})
	}
	const context = 3
	keep := make([]bool, len(ops))
	for k, o := range ops {
		if o.tag == ' ' {
			continue
		}
		for d := -context; d <= context; d++ {
			if k+d >= 0 && k+d < len(ops) {
				keep[k+d] = true
			}
		}
	}
	var out strings.Builder
	out.WriteString("--- " + goldenPath + " (reviewed)\n+++ the compiled descriptor\n")
	skipped := false
	for k, o := range ops {
		if !keep[k] {
			skipped = true
			continue
		}
		if skipped {
			out.WriteString("@@ ... @@\n")
			skipped = false
		}
		out.WriteString(string(o.tag) + o.text + "\n")
	}
	return out.String()
}

// TestContractMatchesItsReviewedGolden regenerates the listing of the compiled contract in memory and compares it
// byte for byte with exec.contract.golden, the listing a reviewer read.
func TestContractMatchesItsReviewedGolden(t *testing.T) {
	raw, readErr := os.ReadFile(goldenPath)
	version := uint32(1)
	if readErr == nil {
		v, err := goldenContractVersion(string(raw))
		if err != nil {
			t.Fatalf("%s: %v", goldenPath, err)
		}
		version = v
	} else if !*update {
		t.Fatalf("read the golden: %v\nregenerate it with: go test ./proto/exec/v2 -run TestContractMatchesItsReviewedGolden -update", readErr)
	}
	got := listing(version)
	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (contract_version %d, %d lines): review the diff before you commit it", goldenPath, version, strings.Count(got, "\n"))
		return
	}
	if got == string(raw) {
		return
	}
	t.Errorf("the compiled contract (proto/exec/v2/exec.proto) differs from its reviewed golden %s:\n\n%s\n"+
		"If this contract is UNRELEASED (no tag carries it), regenerate the golden with\n"+
		"    go test ./proto/exec/v2 -run TestContractMatchesItsReviewedGolden -update\n"+
		"and review the diff of the golden in the pull request.\n"+
		"If this contract is RELEASED, the change is BREAKING: a released message, field number, enum value or RPC never\n"+
		"changes meaning and is never reused. Add what you need under a new number instead (an additive change still needs\n"+
		"the golden regenerated and exec.ContractVersion raised, with the version in the golden's first line).",
		goldenPath, unifiedDiff(string(raw), got))
}

// A repeated scalar, bool or enum field is packed or not on the wire, and the two encodings are different bytes: the
// listing records the effective state, so that a [packed = false] on one of them is a change of the golden.
func TestTheListingRecordsThePackedStateOfRepeatedScalars(t *testing.T) {
	build := func(t *testing.T, packed *bool) protoreflect.MessageDescriptor {
		t.Helper()
		repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		field := func(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
			f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Label: repeated, Type: kind.Enum()}
			if typeName != "" {
				f.TypeName = proto.String(typeName)
			}
			if packed != nil && kind != descriptorpb.FieldDescriptorProto_TYPE_STRING && kind != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
				f.Options = &descriptorpb.FieldOptions{Packed: packed}
			}
			return f
		}
		fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
			Name:    proto.String("packed_probe.proto"),
			Package: proto.String("probe"),
			Syntax:  proto.String("proto3"),
			EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("E"), Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("E_UNSPECIFIED"), Number: proto.Int32(0)},
			}}},
			MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("M"), Field: []*descriptorpb.FieldDescriptorProto{
				field("u", 1, descriptorpb.FieldDescriptorProto_TYPE_UINT32, ""),
				field("b", 2, descriptorpb.FieldDescriptorProto_TYPE_BOOL, ""),
				field("e", 3, descriptorpb.FieldDescriptorProto_TYPE_ENUM, ".probe.E"),
				field("s", 4, descriptorpb.FieldDescriptorProto_TYPE_STRING, ""),
				field("m", 5, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, ".probe.M"),
			}}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return fd.Messages().ByName("M")
	}
	byDefault := build(t, nil)
	notPacked := build(t, proto.Bool(false))
	for _, name := range []protoreflect.Name{"u", "b", "e"} {
		packed, unpacked := fieldText(byDefault.Fields().ByName(name)), fieldText(notPacked.Fields().ByName(name))
		if !strings.Contains(packed, "packed ") || strings.Contains(packed, "unpacked") {
			t.Errorf("a repeated %s in proto3 is packed by default; the listing says %q", name, packed)
		}
		if !strings.Contains(unpacked, "unpacked ") {
			t.Errorf("a repeated %s with [packed = false] is unpacked; the listing says %q", name, unpacked)
		}
		if packed == unpacked {
			t.Errorf("the listing of %s is the same for packed and unpacked: %q", name, packed)
		}
	}
	// A string, a message and a map are never packed: their listing does not say so.
	for _, name := range []protoreflect.Name{"s", "m"} {
		if text := fieldText(byDefault.Fields().ByName(name)); strings.Contains(text, "packed") {
			t.Errorf("repeated %s cannot be packed; the listing says %q", name, text)
		}
	}

	// The real field: kitty_keyboard is packed (one length-delimited run), which is what the listing must say.
	kitty := field(t, message(t, "TerminalModes"), "kitty_keyboard")
	if text := fieldText(kitty); text != "repeated packed uint32" {
		t.Errorf("TerminalModes.kitty_keyboard is listed as %q, want %q", text, "repeated packed uint32")
	}
	modes := &execv2.TerminalModes{KittyKeyboard: []uint32{1, 2}}
	raw, err := proto.Marshal(modes)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{byte(kitty.Number()<<3 | 2), 2, 1, 2}; !slices.Equal(raw, want) {
		t.Errorf("TerminalModes{kitty_keyboard: [1, 2]} encodes as % x, want % x (packed)", raw, want)
	}
}

// TestTheGoldenCarriesTheContractVersion pins the shape of the header that pkg/v1/exec compares with
// exec.ContractVersion.
func TestTheGoldenCarriesTheContractVersion(t *testing.T) {
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goldenContractVersion(string(raw)); err != nil {
		t.Fatalf("%s: %v", goldenPath, err)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// ABSENCE IS UNKNOWN
// ---------------------------------------------------------------------------------------------------------------

type category string

const (
	// intent: a request the host makes; its zero is the documented default.
	catIntent category = "intent"
	// limit: a declared bound; zero is unstated.
	catLimit category = "limit"
	// key: an identifier or a sequence.
	catKey category = "key"
	// counter: observed by the SDK; zero is a count.
	catCounter category = "counter"
	// unit: zero is not a real value (sizes, pid, pgid, timestamps, signal numbers).
	catUnit category = "unit"
	// declared: a bool the plugin's author states and knows; false is "no", never unknown.
	catDeclared category = "declared"
	// observed: a value the SDK itself computes and always knows.
	catObserved category = "observed"
	// arm: the field of an arm that exists only when the fact is known.
	catArm category = "arm"
	// tracked: a field of a TerminalModes that is present, so tracked.
	catTracked category = "tracked"
)

var categories = []category{catIntent, catLimit, catKey, catCounter, catUnit, catDeclared, catObserved, catArm, catTracked}

type auditEntry struct {
	category category
	reason   string
}

func intent(reason string) auditEntry   { return auditEntry{catIntent, reason} }
func limit(reason string) auditEntry    { return auditEntry{catLimit, reason} }
func key(reason string) auditEntry      { return auditEntry{catKey, reason} }
func counter(reason string) auditEntry  { return auditEntry{catCounter, reason} }
func unit(reason string) auditEntry     { return auditEntry{catUnit, reason} }
func declared(reason string) auditEntry { return auditEntry{catDeclared, reason} }
func observed(reason string) auditEntry { return auditEntry{catObserved, reason} }
func arm(reason string) auditEntry      { return auditEntry{catArm, reason} }
func tracked(reason string) auditEntry  { return auditEntry{catTracked, reason} }

// audit lists every bool, numeric and bytes field that is neither proto3 optional nor a oneof member, with the
// reason why its zero value is not "unknown". A new field of such a type fails TestAbsenceIsUnknown until it is made
// optional (or a oneof member) or is listed here, in review, with its reason.
var audit = map[string]auditEntry{
	"Size.cols": unit("zero is no size; a size with one zero dimension is invalid"),
	"Size.rows": unit("zero is no size; a size with one zero dimension is invalid"),

	"ErrorFacts.retry_after_ms": unit("zero means no advice, never \"retry now\""),

	"SessionLimits.max_sessions":          limit("zero reads as unstated, never unlimited"),
	"SessionLimits.max_input_frame":       limit("zero reads as unstated, never unlimited"),
	"SessionLimits.replay_bytes":          limit("zero reads as unstated, never unlimited"),
	"SessionLimits.max_replay_bytes":      limit("zero reads as unstated, never unlimited"),
	"SessionLimits.detach_timeout_ms":     limit("zero reads as unstated, never unlimited"),
	"SessionLimits.max_detach_timeout_ms": limit("zero reads as unstated, never unlimited"),
	"SessionLimits.max_dimension":         limit("zero reads as unstated, never unlimited"),

	"RunLimits.default_timeout_ms":   limit("zero reads as unstated, never unlimited"),
	"RunLimits.max_timeout_ms":       limit("zero reads as unstated, never unlimited"),
	"RunLimits.default_output_bytes": limit("zero reads as unstated, never unlimited"),
	"RunLimits.max_output_bytes":     limit("zero reads as unstated, never unlimited"),
	"RunLimits.max_stdin_bytes":      limit("zero reads as unstated, never unlimited"),
	"RunLimits.max_concurrent":       limit("zero reads as unstated, never unlimited"),
	"RunLimits.max_argv_bytes":       limit("zero reads as unstated, never unlimited"),
	"RunLimits.max_env_bytes":        limit("zero reads as unstated, never unlimited"),
	"RunLimits.post_exit_drain_ms":   limit("zero reads as unstated, never unlimited"),

	"DescribeResponse.contract_version":   key("the version of the contract; zero is not a version (the first release is 1)"),
	"OptionsResponse.observed_at_unix_ms": unit("a timestamp of the SDK's clock; zero is not a time"),

	"Choice.is_default": declared("the author states which choice is the default; false is \"not the default\", never unknown"),
	"Option.required":   declared("the author states whether the option is required; false is \"optional\", never unknown"),
	"Choice.no_program": declared("the author states whether a session that selects the choice runs no program; false is \"it runs the program the request names\", never unknown"),

	"IntegrationRequest.consent":    intent("the host's assertion of consent; false is no consent"),
	"Start.tty":                     intent("a request for a terminal; false asks for none"),
	"Start.detach_timeout_ms":       intent("zero means SessionLimits.detach_timeout_ms"),
	"Start.replay_bytes":            intent("zero means SessionLimits.replay_bytes"),
	"Attach.from_offset":            intent("the first output offset the host does not have; zero asks for everything the ring holds"),
	"Resize.seq":                    key("chosen by the host and echoed in Resized"),
	"Detach.lease_ms":               intent("zero means SessionLimits.detach_timeout_ms"),
	"Query.id":                      key("chosen by the host, at least 1, echoed in the answering Fact"),
	"ReadOutputRequest.from_offset": intent("the cursor of the read; zero reads from the oldest offset the ring holds"),
	"ReadOutputRequest.max_bytes":   intent("zero means the SDK's default"),
	"OutputLimits.max_stdout_bytes": intent("zero means the target's default"),
	"OutputLimits.max_stderr_bytes": intent("zero means the target's default"),
	"RunRequest.stdin":              intent("empty means no stdin"),
	"RunRequest.timeout_ms":         intent("zero means RunLimits.default_timeout_ms"),

	"Detached.lease_expires_unix_ms":  unit("a timestamp; zero when no lease runs (superseded)"),
	"SessionFacts.created_at_unix_ms": unit("a timestamp; zero is not a time"),
	"SessionFacts.pid":                unit("a process id when the producer has one; zero is not a pid"),

	"TerminalModes.alt_screen":         tracked("a TerminalModes that is present states every mode it carries: false is off"),
	"TerminalModes.bracketed_paste":    tracked("a TerminalModes that is present states every mode it carries: false is off"),
	"TerminalModes.application_cursor": tracked("a TerminalModes that is present states every mode it carries: false is off"),
	"TerminalModes.focus_events":       tracked("a TerminalModes that is present states every mode it carries: false is off"),
	"TerminalModes.cursor_hidden":      tracked("a TerminalModes that is present states every mode it carries: false is off"),
	"TerminalModes.kitty_keyboard":     tracked("a TerminalModes that is present states the stack; an empty stack is empty"),

	"Replay.start": key("an output offset of the session's single sequence"),
	"Replay.end":   key("an output offset of the session's single sequence"),
	"Replay.bytes": counter("end - start, always; zero is a count"),

	"Output.offset": key("the position of data[0] in the session's single output sequence"),
	"Output.data":   observed("the bytes the SDK read; empty is no bytes"),
	"Resized.seq":   key("echoes Resize.seq"),

	"Mark.at_offset": key("an output offset"),
	"Mark.redraw":    observed("the scanner always knows whether the prompt was redrawn"),

	"Foreground.pgid": unit("a process group; zero is not a pgid and means unknown"),
	"Fact.query_id":   key("the Query.id answered; zero means pushed by the target"),

	"Dropped.from_offset": key("an output offset"),
	"Dropped.bytes":       counter("the number of bytes dropped; a count"),

	"Signaled.signal_number": unit("a signal number; zero is not a signal and means not reported"),
	"Exited.exit_code":       arm("the Exited arm exists only with a status that a target or a transport relayed (from the transport, code 0 is \"ended normally\"): a stream with none is Lost, never exit 0"),

	"Ended.start_observed": observed("the SDK always knows whether it observed the start"),
	"Ended.duration_ms":    counter("measured by the SDK from the accepted Open to the end"),
	"Ended.bytes_out":      counter("the bytes the SDK read from the command; a count"),
	"Ended.bytes_in":       counter("the bytes the host sent; a count"),
	"Ended.dropped_bytes":  counter("the bytes in Dropped ranges; a count"),
	"Ended.last_offset":    key("one past the last output byte, an offset"),

	"Link.silent_ms": unit("how long nothing has moved; zero when the state is not DEGRADED or STALLED"),

	"SessionInfo.attached":              observed("the SDK always knows whether a stream is attached"),
	"SessionInfo.next_offset":           key("an output offset"),
	"SessionInfo.replay_start":          key("an output offset"),
	"SessionInfo.last_activity_unix_ms": unit("a timestamp; zero means no activity yet"),
	"SessionInfo.lease_expires_unix_ms": unit("a timestamp; zero while attached"),

	"ReadOutputResponse.replay_start": key("an output offset"),
	"ReadOutputResponse.next_offset":  key("an output offset"),
	"ReadOutputResponse.ended":        observed("the SDK always knows whether the session ended"),

	"OutputAccount.total_bytes":   counter("the bytes the command wrote that arrived; a count"),
	"OutputAccount.kept_bytes":    counter("the bytes returned; a count"),
	"OutputAccount.dropped_bytes": counter("the bytes the cap elided; a count"),
	"OutputAccount.truncated":     observed("derived: dropped_bytes is not zero"),
	"OutputAccount.head_bytes":    counter("the bytes kept at the head; a count"),
	"OutputAccount.tail_bytes":    counter("the bytes kept at the tail; a count"),
	"OutputAccount.valid_utf8":    observed("the SDK checks the bytes it returns"),

	"RunResult.signal_number":  unit("a signal number; zero is not a signal (set for OUTCOME_SIGNALED only)"),
	"RunResult.stdout":         observed("the kept bytes of the stream; empty is no bytes"),
	"RunResult.stderr":         observed("the kept bytes of the stream; empty is no bytes"),
	"RunResult.duration_ms":    counter("measured by the SDK from the accepted request to the end"),
	"RunResult.start_observed": observed("the SDK always knows whether it observed the start"),

	"RunProgress.elapsed_ms": counter("measured by the SDK"),
	"RunProgress.bytes_out":  counter("the bytes the command wrote so far; a count"),
}

func isScalarFact(fd protoreflect.FieldDescriptor) bool {
	switch fd.Kind() {
	case protoreflect.BoolKind, protoreflect.BytesKind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
		protoreflect.FloatKind, protoreflect.DoubleKind:
		return !fd.IsMap()
	}
	return false
}

func inRealOneof(fd protoreflect.FieldDescriptor) bool {
	oneof := fd.ContainingOneof()
	return oneof != nil && !oneof.IsSynthetic()
}

func fieldKey(m protoreflect.MessageDescriptor, fd protoreflect.FieldDescriptor) string {
	return relName(m.FullName()) + "." + string(fd.Name())
}

// reachable lists every message that can be reached from the roots through message fields, the messages of other
// files included.
func reachable(roots ...protoreflect.MessageDescriptor) map[protoreflect.FullName]protoreflect.MessageDescriptor {
	seen := map[protoreflect.FullName]protoreflect.MessageDescriptor{}
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if _, ok := seen[m.FullName()]; ok {
			return
		}
		seen[m.FullName()] = m
		for i := range m.Fields().Len() {
			fd := m.Fields().Get(i)
			switch {
			case fd.IsMap():
				if v := fd.MapValue(); v.Message() != nil {
					walk(v.Message())
				}
			case fd.Message() != nil:
				walk(fd.Message())
			}
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return seen
}

// rpcMessages lists the request types (the input of every RPC) and the response types (the output of every RPC).
func rpcMessages() (requests, responses []protoreflect.MessageDescriptor) {
	services := file.Services()
	for i := range services.Len() {
		methods := services.Get(i).Methods()
		for j := range methods.Len() {
			requests = append(requests, methods.Get(j).Input())
			responses = append(responses, methods.Get(j).Output())
		}
	}
	return requests, responses
}

// requestOnly lists the messages of this file that are reachable from a request and from no response: every field of
// them is something the host asks for.
func requestOnly() map[protoreflect.FullName]bool {
	requests, responses := rpcMessages()
	fromResponses := reachable(responses...)
	out := map[protoreflect.FullName]bool{}
	for name := range reachable(requests...) {
		if _, shared := fromResponses[name]; !shared && name.Parent() == file.Package() {
			out[name] = true
		}
	}
	return out
}

func enumPrefix(name protoreflect.Name) string {
	var b strings.Builder
	for i, r := range string(name) {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// unknownEncoding says how a field writes "not known".
func unknownEncoding(fd protoreflect.FieldDescriptor, cat category) string {
	switch {
	case fd.HasOptionalKeyword():
		return "optional (field absent)"
	case inRealOneof(fd):
		return "arm (arm unset)"
	case fd.IsMap():
		return "map (empty)"
	case fd.Cardinality() == protoreflect.Repeated && fd.Message() != nil:
		return "repeated (empty)"
	case fd.Message() != nil && (fd.Message().Name() == "SignalSet" || fd.Message().Name() == "IntegrationModeSet"):
		return "wrapped set (absent; present empty is none)"
	case fd.Message() != nil:
		return "absent message"
	case fd.Kind() == protoreflect.EnumKind && fd.Cardinality() == protoreflect.Repeated:
		return "repeated (empty)"
	case fd.Kind() == protoreflect.EnumKind:
		return "UNSPECIFIED enum"
	case cat == catArm:
		return "arm (the arm exists only with the fact)"
	default:
		return "zero (" + string(cat) + ")"
	}
}

// factRow is one row of the table of TestAbsenceIsUnknown.
type factRow struct {
	message, field, encoding string
	category                 category
	fact                     bool
}

// absenceTable lists every bool, numeric, bytes, enum and message field of the file (a string is an identifier or
// text: empty is the text's own unknown) with how it writes "unknown" and its category: the audit's for a scalar and
// "fact" or "intent" for the others.
func absenceTable() []factRow {
	onlyRequests := requestOnly()
	var rows []factRow
	for _, m := range allMessages() {
		for _, fd := range fieldsByNumber(m) {
			if fd.Kind() == protoreflect.StringKind && !fd.IsMap() {
				continue
			}
			cat := category("fact")
			if onlyRequests[m.FullName()] {
				cat = catIntent
			}
			if e, ok := audit[fieldKey(m, fd)]; ok {
				cat = e.category
			}
			rows = append(rows, factRow{
				message:  relName(m.FullName()),
				field:    string(fd.Name()),
				encoding: unknownEncoding(fd, cat),
				category: cat,
				fact:     cat != catIntent && cat != catLimit && cat != catKey,
			})
		}
	}
	return rows
}

// TestAbsenceIsUnknown enforces the one rule of the file header: a fact that a producer may not know is written so that
// "not known" is a value of its own. It walks every message field of the descriptor.
func TestAbsenceIsUnknown(t *testing.T) {
	// 1. Every enum starts at zero with <ENUM>_UNSPECIFIED, and every value carries the enum's prefix.
	for _, e := range allEnums() {
		prefix := enumPrefix(e.Name())
		first := e.Values().Get(0)
		if first.Number() != 0 || string(first.Name()) != prefix+"_UNSPECIFIED" {
			t.Errorf("enum %s: the first value is %s = %d, want %s_UNSPECIFIED = 0", relName(e.FullName()), first.Name(), first.Number(), prefix)
		}
		for i := range e.Values().Len() {
			if v := e.Values().Get(i); !strings.HasPrefix(string(v.Name()), prefix+"_") {
				t.Errorf("enum %s: value %s does not start with %s_", relName(e.FullName()), v.Name(), prefix)
			}
		}
	}

	// 2. Every bool, numeric and bytes field is optional, a oneof member, or audited.
	required := map[string]bool{}
	for _, m := range allMessages() {
		for _, fd := range fieldsByNumber(m) {
			if !isScalarFact(fd) {
				continue
			}
			k := fieldKey(m, fd)
			if fd.HasOptionalKeyword() || inRealOneof(fd) {
				if _, listed := audit[k]; listed {
					t.Errorf("%s is %s and needs no audit entry: remove the entry", k, map[bool]string{true: "optional", false: "a oneof member"}[fd.HasOptionalKeyword()])
				}
				continue
			}
			required[k] = true
			if _, ok := audit[k]; !ok {
				t.Errorf("%s (%s) is neither optional nor a oneof member and is not in the audit table: "+
					"make it optional, or list it with a category and the reason why its zero is not \"unknown\"", k, fd.Kind())
			}
		}
	}

	// 3. No stale entry, a known category, a reason.
	valid := map[category]bool{}
	for _, c := range categories {
		valid[c] = true
	}
	for k, e := range audit {
		if !required[k] {
			t.Errorf("audit entry %s does not name a field that needs one (stale, renamed, made optional or not a scalar)", k)
		}
		if !valid[e.category] {
			t.Errorf("audit entry %s has the unknown category %q", k, e.category)
		}
		if strings.TrimSpace(e.reason) == "" {
			t.Errorf("audit entry %s has no reason", k)
		}
	}

	// 4. The table.
	counts := map[category]int{}
	for _, e := range audit {
		counts[e.category]++
	}
	var summary []string
	for _, c := range categories {
		summary = append(summary, fmt.Sprintf("%s=%d", c, counts[c]))
	}
	t.Logf("audited fields: %d (%s)", len(audit), strings.Join(summary, " "))
	t.Logf("%-44s | %-46s | %s", "field", "unknown encoding", "category")
	for _, row := range absenceTable() {
		t.Logf("%-44s | %-46s | %s", row.message+"."+row.field, row.encoding, row.category)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// Shape tests: one decision of the review each
// ---------------------------------------------------------------------------------------------------------------

func message(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()
	m := file.Messages().ByName(protoreflect.Name(name))
	if m == nil {
		t.Fatalf("message %s does not exist", name)
	}
	return m
}

func enum(t *testing.T, name string) protoreflect.EnumDescriptor {
	t.Helper()
	e := file.Enums().ByName(protoreflect.Name(name))
	if e == nil {
		t.Fatalf("enum %s does not exist", name)
	}
	return e
}

func field(t *testing.T, m protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
	t.Helper()
	fd := m.Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		t.Fatalf("field %s.%s does not exist", m.Name(), name)
	}
	return fd
}

func enumHas(e protoreflect.EnumDescriptor, value string) bool {
	return e.Values().ByName(protoreflect.Name(value)) != nil
}

// wantMessageField asserts a singular message-typed field of the named message type.
func wantMessageField(t *testing.T, m protoreflect.MessageDescriptor, name, typ string) {
	t.Helper()
	fd := field(t, m, name)
	if fd.Message() == nil || fd.IsMap() || fd.Cardinality() == protoreflect.Repeated || fd.Message().Name() != protoreflect.Name(typ) {
		t.Errorf("%s.%s is %s, want a singular message %s", m.Name(), name, fieldText(fd), typ)
	}
}

// wantOptional asserts a proto3-optional field of the given kind.
func wantOptional(t *testing.T, m protoreflect.MessageDescriptor, name string, kind protoreflect.Kind) {
	t.Helper()
	fd := field(t, m, name)
	if !fd.HasOptionalKeyword() || fd.Kind() != kind {
		t.Errorf("%s.%s is %s, want optional %s", m.Name(), name, fieldText(fd), kind)
	}
}

// B1: a failure after the command was handed to the target is a RESULT, not a status.
func TestBlocker1FailureAfterHandOffIsAResult(t *testing.T) {
	if !enumHas(enum(t, "Outcome"), "OUTCOME_FAILED") {
		t.Error("enum Outcome has no OUTCOME_FAILED: a run that failed after it was handed to the target has no result")
	}
	runResult := message(t, "RunResult")
	wantMessageField(t, runResult, "failed", "Failed")
	wantMessageField(t, message(t, "RunEnded"), "result", "RunResult")
	// The result keeps what is known when the run failed.
	for _, name := range []string{"stdout", "stderr", "stdout_account", "stderr_account", "start_observed"} {
		field(t, runResult, name)
	}
}

// B2: whether the remote process may survive is a tri-state, never a bool that reads false as "gone".
func TestBlocker2RemoteMaySurviveIsOptionalBool(t *testing.T) {
	wantOptional(t, message(t, "End"), "remote_may_survive", protoreflect.BoolKind)
	wantOptional(t, message(t, "RunResult"), "remote_may_survive", protoreflect.BoolKind)
}

// B3: a superseded stream is detached, not ended; Ended is the end of the session, at most once (when no stream was
// attached at the end, the tombstone carries it).
func TestBlocker3SupersededIsADetachNotAnEnd(t *testing.T) {
	for i, closedBy := 0, enum(t, "ClosedBy"); i < closedBy.Values().Len(); i++ {
		if name := closedBy.Values().Get(i).Name(); strings.Contains(string(name), "SUPERSEDED") {
			t.Errorf("enum ClosedBy has %s: a superseded stream is Detached{reason}, never an end", name)
		}
	}
	if !enumHas(enum(t, "DetachReason"), "DETACH_REASON_SUPERSEDED") {
		t.Error("enum DetachReason has no DETACH_REASON_SUPERSEDED")
	}
	reason := field(t, message(t, "Detached"), "reason")
	if reason.Enum() == nil || reason.Enum().Name() != "DetachReason" || reason.Cardinality() == protoreflect.Repeated {
		t.Errorf("Detached.reason is %s, want enum DetachReason", fieldText(reason))
	}

	// Every arm of ServerFrame is classified; the terminal ones are exactly ended and detached.
	terminal := map[string]bool{
		"opened": false, "output": false, "resized": false, "mark": false, "fact": false, "dropped": false,
		"notice": false, "ended": true, "started": false, "link": false, "detached": true,
	}
	var gotTerminal []string
	serverFrame := message(t, "ServerFrame")
	for _, fd := range fieldsByNumber(serverFrame) {
		isTerminal, known := terminal[string(fd.Name())]
		switch {
		case !known:
			t.Errorf("ServerFrame has the new arm %q: say in this test whether it is terminal (the last frame of a stream); "+
				"a stream ends with exactly one of Ended and Detached", fd.Name())
		case isTerminal:
			gotTerminal = append(gotTerminal, string(fd.Name()))
		}
	}
	slices.Sort(gotTerminal)
	if want := []string{"detached", "ended"}; !slices.Equal(gotTerminal, want) {
		t.Errorf("ServerFrame terminal arms are %v, want exactly %v", gotTerminal, want)
	}
	for name := range terminal {
		if serverFrame.Fields().ByName(protoreflect.Name(name)) == nil {
			t.Errorf("ServerFrame has no arm %q", name)
		}
	}
	for _, problem := range terminalArmsOneofProblems(serverFrame) {
		t.Error(problem)
	}

	// Ended is the end of the session: only the frame and the tombstone carry it.
	var holders []string
	for _, m := range allMessages() {
		for _, fd := range fieldsByNumber(m) {
			if fd.Message() != nil && fd.Message().Name() == "Ended" {
				holders = append(holders, fieldKey(m, fd))
			}
		}
	}
	slices.Sort(holders)
	if want := []string{"ServerFrame.ended", "SessionInfo.ended_with"}; !slices.Equal(holders, want) {
		t.Errorf("message Ended is a field of %v, want exactly %v (the frame and the tombstone)", holders, want)
	}
}

// A session has exactly one END and at most one Ended frame: a session that ends while no stream is attached (a lease
// that expires, a retirement, a shutdown) has no stream to carry one, and its end is the tombstone's. No comment of
// the contract says that Ended is sent exactly once, which only a session that ends with a stream attached satisfies.
func TestNoCommentSaysThatASessionHasExactlyOneEnded(t *testing.T) {
	raw, err := os.ReadFile("exec.proto")
	if err != nil {
		t.Fatal(err)
	}
	// The comments are wrapped: read them as one run of words, so that a phrase that spans two lines is found.
	words := strings.Join(strings.Fields(strings.ReplaceAll(string(raw), "//", " ")), " ")
	for _, phrase := range []string{"exactly one Ended", "Ended exactly once", "sent exactly once", "SDK, exactly once"} {
		if strings.Contains(words, phrase) {
			t.Errorf("exec.proto says %q: a session that ends while no stream is attached has no Ended frame, so Ended is sent at most once", phrase)
		}
	}
	if !strings.Contains(words, "AT MOST ONE Ended") {
		t.Error("exec.proto does not say that a session has AT MOST ONE Ended")
	}
}

// terminalArmsOneofProblems checks that ended and detached are arms of the same real (not synthetic) oneof of the
// frame, named "frame": that is what makes the last frame of a stream exactly one of them, and never both. Each arm
// carries the message of its own name (Ended, Detached): a frame whose detached arm held another message could not
// report why the session was detached.
func terminalArmsOneofProblems(frame protoreflect.MessageDescriptor) []string {
	var problems []string
	var shared protoreflect.OneofDescriptor
	for _, name := range []protoreflect.Name{"ended", "detached"} {
		fd := frame.Fields().ByName(name)
		if fd == nil {
			problems = append(problems, fmt.Sprintf("%s has no arm %q", frame.Name(), name))
			continue
		}
		want := frame.FullName().Parent().Append(protoreflect.Name(strings.ToUpper(string(name[:1])) + string(name[1:])))
		if fd.Message() == nil || fd.Message().FullName() != want {
			got := protoreflect.FullName("no message")
			if fd.Message() != nil {
				got = fd.Message().FullName()
			}
			problems = append(problems, fmt.Sprintf("%s.%s carries %s, want %s", frame.Name(), name, got, want))
		}
		oneof := fd.ContainingOneof()
		switch {
		case oneof == nil:
			problems = append(problems, fmt.Sprintf("%s.%s is not a member of a oneof: a frame could carry it with another arm", frame.Name(), name))
			continue
		case oneof.IsSynthetic():
			problems = append(problems, fmt.Sprintf("%s.%s is in the synthetic oneof %q of an optional field, not in the oneof frame", frame.Name(), name, oneof.Name()))
			continue
		case oneof.Name() != "frame":
			problems = append(problems, fmt.Sprintf("%s.%s is in the oneof %q, want the oneof frame", frame.Name(), name, oneof.Name()))
			continue
		}
		if shared != nil && shared.FullName() != oneof.FullName() {
			problems = append(problems, fmt.Sprintf("%s.ended and %s.detached are in different oneofs (%s, %s)", frame.Name(), frame.Name(), shared.Name(), oneof.Name()))
		}
		shared = oneof
	}
	return problems
}

// The check of the terminal arms must see a descriptor in which they are not in the oneof: ServerFrame is rebuilt from
// the compiled descriptor with its arms taken out of the oneof, or moved to another, and each of them is a problem.
func TestBlocker3TerminalArmsCheckSeesArmsOutsideTheFrameOneof(t *testing.T) {
	rebuild := func(t *testing.T, change func(fields map[string]*descriptorpb.FieldDescriptorProto, serverFrame *descriptorpb.DescriptorProto)) protoreflect.MessageDescriptor {
		t.Helper()
		fdp := protodesc.ToFileDescriptorProto(file)
		for _, m := range fdp.MessageType {
			if m.GetName() != "ServerFrame" {
				continue
			}
			fields := map[string]*descriptorpb.FieldDescriptorProto{}
			for _, f := range m.Field {
				fields[f.GetName()] = f
			}
			change(fields, m)
			// The arms of a oneof must be declared one after the other: put the fields in the order of their oneofs,
			// and those of none last.
			slices.SortStableFunc(m.Field, func(a, b *descriptorpb.FieldDescriptorProto) int {
				key := func(f *descriptorpb.FieldDescriptorProto) int32 {
					if f.OneofIndex == nil {
						return 1 << 30
					}
					return f.GetOneofIndex()
				}
				return int(key(a) - key(b))
			})
			rebuilt, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
			if err != nil {
				t.Fatal(err)
			}
			return rebuilt.Messages().ByName("ServerFrame")
		}
		t.Fatal("no ServerFrame")
		return nil
	}

	if problems := terminalArmsOneofProblems(rebuild(t, func(map[string]*descriptorpb.FieldDescriptorProto, *descriptorpb.DescriptorProto) {})); len(problems) != 0 {
		t.Fatalf("the unchanged ServerFrame has problems: %v", problems)
	}
	for name, c := range map[string]struct {
		change func(map[string]*descriptorpb.FieldDescriptorProto, *descriptorpb.DescriptorProto)
		want   int
	}{
		"ended outside any oneof": {func(f map[string]*descriptorpb.FieldDescriptorProto, _ *descriptorpb.DescriptorProto) {
			f["ended"].OneofIndex = nil
		}, 1},
		"detached outside any oneof": {func(f map[string]*descriptorpb.FieldDescriptorProto, _ *descriptorpb.DescriptorProto) {
			f["detached"].OneofIndex = nil
		}, 1},
		"both outside any oneof": {func(f map[string]*descriptorpb.FieldDescriptorProto, _ *descriptorpb.DescriptorProto) {
			f["ended"].OneofIndex = nil
			f["detached"].OneofIndex = nil
		}, 2},
		"detached in a oneof of its own": {func(f map[string]*descriptorpb.FieldDescriptorProto, m *descriptorpb.DescriptorProto) {
			m.OneofDecl = append(m.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String("closing")})
			f["detached"].OneofIndex = proto.Int32(int32(len(m.OneofDecl) - 1))
		}, 1},
		"both in a oneof of another name": {func(f map[string]*descriptorpb.FieldDescriptorProto, m *descriptorpb.DescriptorProto) {
			m.OneofDecl = append(m.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String("closing")})
			index := proto.Int32(int32(len(m.OneofDecl) - 1))
			f["ended"].OneofIndex = index
			f["detached"].OneofIndex = index
		}, 2},
		"detached carries Notice": {func(f map[string]*descriptorpb.FieldDescriptorProto, _ *descriptorpb.DescriptorProto) {
			f["detached"].TypeName = proto.String(f["notice"].GetTypeName())
		}, 1},
		"ended carries Detached": {func(f map[string]*descriptorpb.FieldDescriptorProto, _ *descriptorpb.DescriptorProto) {
			f["ended"].TypeName = proto.String(f["detached"].GetTypeName())
		}, 1},
		"ended and detached swap their messages": {func(f map[string]*descriptorpb.FieldDescriptorProto, _ *descriptorpb.DescriptorProto) {
			f["ended"].TypeName, f["detached"].TypeName = f["detached"].TypeName, f["ended"].TypeName
		}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			problems := terminalArmsOneofProblems(rebuild(t, c.change))
			if len(problems) != c.want {
				t.Errorf("got %d problems %v, want %d", len(problems), problems, c.want)
			}
		})
	}
}

// B4: capabilities have two levels and a session can narrow every one of them.
func TestBlocker4CapabilitiesAreEffectivePerSessionAndNarrowable(t *testing.T) {
	wantMessageField(t, message(t, "SessionFacts"), "capabilities", "Capabilities")
	wantMessageField(t, message(t, "Started"), "facts", "SessionFacts")
	wantMessageField(t, message(t, "TargetInfo"), "capabilities", "Capabilities")

	// Narrowing and unknown are only expressible when every field has presence, an UNSPECIFIED zero or is a message.
	caps := message(t, "Capabilities")
	for _, fd := range fieldsByNumber(caps) {
		switch {
		case fd.HasOptionalKeyword():
		case fd.Kind() == protoreflect.EnumKind && fd.Cardinality() != protoreflect.Repeated:
			first := fd.Enum().Values().Get(0)
			if first.Number() != 0 || !strings.HasSuffix(string(first.Name()), "_UNSPECIFIED") {
				t.Errorf("Capabilities.%s: enum %s starts with %s, want an UNSPECIFIED zero", fd.Name(), fd.Enum().Name(), first.Name())
			}
		case fd.Kind() == protoreflect.MessageKind && fd.Cardinality() != protoreflect.Repeated && !fd.IsMap():
		default:
			t.Errorf("Capabilities.%s is %s: a capability needs presence (optional), an UNSPECIFIED enum or a message, "+
				"or it cannot be narrowed and its unknown is not expressible", fd.Name(), fieldText(fd))
		}
	}
}

// The mode of a session (a Kubernetes exec or attach) is declared on the choice of an option, number 7 of Choice, and
// is a message: absent means the choice does not narrow, which a bool or an enum could not say.
func TestAChoiceCarriesItsModeAsCapabilities(t *testing.T) {
	choice := message(t, "Choice")
	wantMessageField(t, choice, "capabilities", "Capabilities")
	if got := field(t, choice, "capabilities").Number(); got != 7 {
		t.Errorf("Choice.capabilities is field %d, want 7", got)
	}
}

// B5: one rule, absence is unknown: the specific fields the review named.
func TestBlocker5SpecificFieldsCarryPresence(t *testing.T) {
	caps := message(t, "Capabilities")
	for _, name := range []string{"tty", "separate_stderr", "resize", "initial_size", "stdin_eof", "env", "cwd", "link_health"} {
		wantOptional(t, caps, name, protoreflect.BoolKind)
	}
	wantMessageField(t, caps, "signals", "SignalSet")
	wantMessageField(t, caps, "integration", "IntegrationModeSet")

	wantOptional(t, message(t, "Signaled"), "core_dumped", protoreflect.BoolKind)
	wantOptional(t, message(t, "SessionFacts"), "tty", protoreflect.BoolKind)
	wantOptional(t, message(t, "Dropped"), "mode_reset", protoreflect.BytesKind)
	wantOptional(t, message(t, "Ended"), "mode_reset", protoreflect.BytesKind)

	modes := message(t, "TerminalModes")
	if modes.Fields().ByName("known") != nil {
		t.Error("TerminalModes has a field named known: the absence of the message is \"not tracked\"")
	}
	for name, enumName := range map[string]string{"mouse_tracking": "MouseTracking", "mouse_encoding": "MouseEncoding"} {
		fd := field(t, modes, name)
		if fd.Enum() == nil || fd.Enum().Name() != protoreflect.Name(enumName) {
			t.Errorf("TerminalModes.%s is %s, want enum %s", name, fieldText(fd), enumName)
		}
	}

	// Exit codes carry every platform's status exactly; the arm itself says it is known.
	exited := field(t, message(t, "Exited"), "exit_code")
	if exited.Kind() != protoreflect.Sint64Kind || exited.HasOptionalKeyword() || exited.Cardinality() == protoreflect.Repeated {
		t.Errorf("Exited.exit_code is %s, want a plain sint64 (the arm exists only with a status)", fieldText(exited))
	}
	wantOptional(t, message(t, "Mark"), "exit_code", protoreflect.Sint64Kind)
	wantOptional(t, message(t, "RunResult"), "exit_code", protoreflect.Sint64Kind)
}

// (a) Requests are strict: a plugin refuses a field it does not know, at any depth, and a map entry would hide one.
func TestStrictRequestsCarryNoMap(t *testing.T) {
	want := []string{"ClientFrame", "DescribeRequest", "EndSessionRequest", "ListSessionsRequest", "OptionsRequest", "ReadOutputRequest", "RunRequest"}
	requests, _ := rpcMessages()
	var got []string
	for _, r := range requests {
		got = append(got, string(r.Name()))
	}
	slices.Sort(got)
	got = slices.Compact(got)
	if !slices.Equal(got, want) {
		t.Fatalf("the request types of the service are %v, want %v: a new RPC needs its request type reviewed for strictness (no map)", got, want)
	}
	for _, name := range want {
		for _, m := range reachable(message(t, name)) {
			for _, fd := range fieldsByNumber(m) {
				if fd.IsMap() {
					t.Errorf("%s is reachable from the request %s and is a map: use a repeated NameValue, whose unknown fields a plugin can see", fieldKey(m, fd), name)
				}
			}
		}
	}
}

// (b) Names.
func TestNamesReasonAndFeatureReplayAreGone(t *testing.T) {
	if file.Enums().ByName("Reason") != nil {
		t.Error("enum Reason exists: it is ErrorReason")
	}
	if enum(t, "ErrorReason").Values().Get(0).Name() != "ERROR_REASON_UNSPECIFIED" {
		t.Error("enum ErrorReason does not start with ERROR_REASON_UNSPECIFIED")
	}
	for _, e := range allEnums() {
		for i := range e.Values().Len() {
			if name := e.Values().Get(i).Name(); name == "FEATURE_REPLAY" || strings.Contains(string(name), "REPLAY") && e.Name() == "Feature" {
				t.Errorf("enum %s has the value %s: the feature is FEATURE_SESSION_MANAGEMENT", e.Name(), name)
			}
		}
	}
	if !enumHas(enum(t, "Feature"), "FEATURE_SESSION_MANAGEMENT") {
		t.Error("enum Feature has no FEATURE_SESSION_MANAGEMENT")
	}
}
