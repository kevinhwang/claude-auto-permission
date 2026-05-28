// Package loader resolves a config message in layered precedence: schema defaults → textproto file → env vars → CLI
// flags. Bindings are declared on the schema via the config.v1 FieldOpts annotation; Walk discovers them at startup.
//
// Only scalar singular fields (string, bool, int*, float/double, enum) can carry env_var/cli_flag bindings. Annotating
// a repeated/map/oneof/message field is a startup error.
package loader

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	configpb "claude-auto-permission/internal/gen/config/v1"
)

// envPrefix is prepended to every derived env-var name. Stable — users hard-code these in shell rcs.
const envPrefix = "CLAUDE_AUTO_PERMISSION_"

// Binding describes one annotated scalar field.
type Binding struct {
	// Path is the dotted path from root, lower-snake segments.
	Path []string

	Field protoreflect.FieldDescriptor

	Description string
	EnvName     string
	FlagName    string
	FlagShort   string

	// Default; Default.IsValid() is false when no default was declared.
	Default protoreflect.Value
}

// Walk returns one Binding per annotated scalar field reachable from md. Field paths include wrapper-message segments
// so leaves with the same name in different parents don't collide on derived env names.
func Walk(md protoreflect.MessageDescriptor) ([]Binding, error) {
	var out []Binding
	if err := walk(md, nil, &out, map[protoreflect.FullName]bool{}); err != nil {
		return nil, err
	}
	return out, nil
}

func walk(md protoreflect.MessageDescriptor, prefix []string, out *[]Binding, seen map[protoreflect.FullName]bool) error {
	// Cycle guard: a self-referencing message would loop forever.
	if seen[md.FullName()] {
		return nil
	}
	seen[md.FullName()] = true
	defer delete(seen, md.FullName())

	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		opts := fieldOpts(fd)

		path := append(append([]string{}, prefix...), string(fd.Name()))

		if opts == nil {
			// Recurse into submessages to discover nested annotations.
			if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() {
				if err := walk(fd.Message(), path, out, seen); err != nil {
					return err
				}
			}
			continue
		}

		if !isScalarSingularField(fd) {
			return fmt.Errorf(
				"loader: field %s: env_var/cli_flag bindings only supported on scalar singular fields (string, bool, int32/64, float/double, enum)",
				fd.FullName())
		}

		def, err := parseDefault(fd, opts.HasDefault(), opts.GetDefault())
		if err != nil {
			return fmt.Errorf("loader: field %s: parse default %q: %w", fd.FullName(), opts.GetDefault(), err)
		}

		b := Binding{
			Path:        path,
			Field:       fd,
			Description: opts.GetDescription(),
			Default:     def,
		}
		if e := opts.GetEnvVar(); e != nil {
			b.EnvName = e.GetName()
			if b.EnvName == "" {
				b.EnvName = deriveEnvName(path)
			}
		}
		if c := opts.GetCliFlag(); c != nil {
			b.FlagName = c.GetName()
			if b.FlagName == "" {
				b.FlagName = deriveFlagName(path)
			}
			b.FlagShort = c.GetShort()
		}
		*out = append(*out, b)
	}
	return nil
}

// fieldOpts returns the FieldOpts extension on fd, or nil.
func fieldOpts(fd protoreflect.FieldDescriptor) *configpb.FieldOpts {
	opts, ok := fd.Options().(proto.Message)
	if !ok || opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, configpb.E_Opts) {
		return nil
	}
	v, _ := proto.GetExtension(opts, configpb.E_Opts).(*configpb.FieldOpts)
	return v
}

// isScalarSingularField reports whether fd is a supported kind for env/flag binding.
func isScalarSingularField(fd protoreflect.FieldDescriptor) bool {
	if fd.IsList() || fd.IsMap() || fd.ContainingOneof() != nil {
		return false
	}
	switch fd.Kind() {
	case protoreflect.StringKind,
		protoreflect.BoolKind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
		protoreflect.FloatKind,
		protoreflect.DoubleKind,
		protoreflect.EnumKind:
		return true
	}
	return false
}

// parseDefault parses the string default from an annotation. Empty raw yields an invalid Value (meaning "no default").
func parseDefault(fd protoreflect.FieldDescriptor, present bool, raw string) (protoreflect.Value, error) {
	if !present {
		return protoreflect.Value{}, nil
	}
	if raw == "" {
		return zeroValue(fd), nil
	}
	return parseScalar(fd, raw)
}

// zeroValue returns the typed zero for fd's kind (empty string, 0, false, etc.).
func zeroValue(fd protoreflect.FieldDescriptor) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString("")
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(false)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(0)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(0)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(0)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(0)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(0)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(0)
	case protoreflect.EnumKind:
		return protoreflect.ValueOfEnum(0)
	}
	return protoreflect.Value{}
}

// parseScalar converts a string to a protoreflect.Value matching fd.Kind().
func parseScalar(fd protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("bool: %w", err)
		}
		return protoreflect.ValueOfBool(b), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("int32: %w", err)
		}
		return protoreflect.ValueOfInt32(int32(n)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("int64: %w", err)
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("uint32: %w", err)
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("uint64: %w", err)
		}
		return protoreflect.ValueOfUint64(n), nil
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("float: %w", err)
		}
		return protoreflect.ValueOfFloat32(float32(f)), nil
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("double: %w", err)
		}
		return protoreflect.ValueOfFloat64(f), nil
	case protoreflect.EnumKind:
		ev := fd.Enum().Values().ByName(protoreflect.Name(raw))
		if ev == nil {
			return protoreflect.Value{}, fmt.Errorf("enum %s: no value named %q", fd.Enum().FullName(), raw)
		}
		return protoreflect.ValueOfEnum(ev.Number()), nil
	}
	return protoreflect.Value{}, fmt.Errorf("unsupported kind %s", fd.Kind())
}

// setByPath walks path and assigns v at the terminal scalar. Intermediate message fields are auto-created.
func setByPath(root protoreflect.Message, path []string, v protoreflect.Value) error {
	if len(path) == 0 {
		return fmt.Errorf("empty path")
	}
	msg := root
	for i, seg := range path {
		fd := msg.Descriptor().Fields().ByName(protoreflect.Name(seg))
		if fd == nil {
			return fmt.Errorf("field %q not found in %s", seg, msg.Descriptor().FullName())
		}
		if i == len(path)-1 {
			msg.Set(fd, v)
			return nil
		}
		// Mutate the submessage in place so the terminal Set lands on the same instance any prior layer wrote to.
		msg = msg.Mutable(fd).Message()
	}
	return nil
}

// deriveEnvName builds CLAUDE_AUTO_PERMISSION_<UPPER_SNAKE_PATH>. Wrapper segments are included so same-named leaves
// don't collide.
func deriveEnvName(path []string) string {
	var b strings.Builder
	b.WriteString(envPrefix)
	for i, seg := range path {
		if i > 0 {
			b.WriteByte('_')
		}
		b.WriteString(strings.ToUpper(seg))
	}
	return b.String()
}

// deriveFlagName uses the kebab-cased leaf only — `--cache-dir` reads better than `--runtime-cache-dir`. Annotation
// overrides resolve the rare collision case.
func deriveFlagName(path []string) string {
	if len(path) == 0 {
		return ""
	}
	leaf := path[len(path)-1]
	return strings.ReplaceAll(leaf, "_", "-")
}
