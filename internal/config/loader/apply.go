package loader

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ApplyDefaults seeds msg with each binding's parsed default value. Bindings without a default leave the corresponding
// field at its proto zero.
func ApplyDefaults(msg proto.Message, bindings []Binding) error {
	root := msg.ProtoReflect()
	for _, b := range bindings {
		if !b.Default.IsValid() {
			continue
		}
		if err := setByPath(root, b.Path, b.Default); err != nil {
			return fmt.Errorf("apply default for %s: %w", strings.Join(b.Path, "."), err)
		}
	}
	return nil
}

// FillDefaults seeds msg with schema-declared defaults for scalar fields that have not been explicitly set (Has returns
// false). Safe to call on a message that already has user-specified values — it won't overwrite them even if they're
// zero.
//
// Unlike ApplyDefaults (which unconditionally sets all defaults as the first step of the Resolve pipeline),
// FillDefaults is designed for standalone messages that didn't go through the full Resolve pipeline (e.g., submessages
// inside repeated fields).
//
// If an intermediate submessage along a binding's path is absent, the field is skipped — FillDefaults never vivifies
// submessages.
func FillDefaults(msg proto.Message) error {
	bindings, err := Walk(msg.ProtoReflect().Descriptor())
	if err != nil {
		return err
	}
	root := msg.ProtoReflect()
	for _, b := range bindings {
		if !b.Default.IsValid() {
			continue
		}
		container, fd := resolveLeaf(root, b.Path)
		if container == nil || fd == nil {
			continue
		}
		if container.Has(fd) {
			continue
		}
		container.Set(fd, b.Default)
	}
	return nil
}

// resolveLeaf walks path from root and returns the containing message and terminal field descriptor. Returns (nil, nil)
// if any intermediate submessage is absent (Has returns false) — this avoids vivifying submessages that the user never
// configured.
func resolveLeaf(root protoreflect.Message, path []string) (protoreflect.Message, protoreflect.FieldDescriptor) {
	if len(path) == 0 {
		return nil, nil
	}
	msg := root
	for i, seg := range path {
		fd := msg.Descriptor().Fields().ByName(protoreflect.Name(seg))
		if fd == nil {
			return nil, nil
		}
		if i == len(path)-1 {
			return msg, fd
		}
		if !msg.Has(fd) {
			return nil, nil
		}
		msg = msg.Get(fd).Message()
	}
	return nil, nil
}

// ApplyFile parses textproto bytes onto msg. Existing field values stay unless the file explicitly sets them —
// prototext.Unmarshal is reset-then-merge, so we unmarshal into a fresh sibling and let proto.Merge copy only the
// populated fields.
func ApplyFile(msg proto.Message, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	tmp := msg.ProtoReflect().New().Interface()
	if err := prototext.Unmarshal(data, tmp); err != nil {
		return err
	}
	proto.Merge(msg, tmp)
	return nil
}

// ApplyEnv overrides msg fields from the env map for every binding that declared an env_var binding and whose env name
// is present. Empty env values are treated as unset to avoid clobbering prior layers with garbage.
func ApplyEnv(msg proto.Message, bindings []Binding, env map[string]string) error {
	root := msg.ProtoReflect()
	for _, b := range bindings {
		if b.EnvName == "" {
			continue
		}
		raw, ok := env[b.EnvName]
		if !ok || raw == "" {
			continue
		}
		v, err := parseScalar(b.Field, raw)
		if err != nil {
			return fmt.Errorf("env %s=%q: %w", b.EnvName, raw, err)
		}
		if err := setByPath(root, b.Path, v); err != nil {
			return fmt.Errorf("apply env %s: %w", b.EnvName, err)
		}
	}
	return nil
}

// ApplyFlags overrides msg fields from fs for every binding whose flag was explicitly set on the command line. Defaults
// registered on the FlagSet are ignored — they were already applied by ApplyDefaults from the schema annotation.
func ApplyFlags(msg proto.Message, bindings []Binding, fs *pflag.FlagSet) error {
	if fs == nil {
		return nil
	}
	byName := map[string]Binding{}
	for _, b := range bindings {
		if b.FlagName != "" {
			byName[b.FlagName] = b
		}
	}
	root := msg.ProtoReflect()
	var applyErr error
	fs.Visit(func(f *pflag.Flag) {
		if applyErr != nil {
			return
		}
		b, ok := byName[f.Name]
		if !ok {
			return
		}
		v, err := parseScalar(b.Field, f.Value.String())
		if err != nil {
			applyErr = fmt.Errorf("flag --%s=%q: %w", f.Name, f.Value.String(), err)
			return
		}
		if err := setByPath(root, b.Path, v); err != nil {
			applyErr = fmt.Errorf("apply flag --%s: %w", f.Name, err)
		}
	})
	return applyErr
}

// settingValue parses raw against fd and updates a sentinel so the flag layer can detect explicit user input via
// pflag.FlagSet.Visit. We don't store the parsed value here; ApplyFlags re-parses from f.Value.String() so the same
// code path runs whether the value was supplied as `--foo=bar` or `--foo bar`.
type settingValue struct {
	fd      protoreflect.FieldDescriptor
	current string // last value Set() saw, used by String() for --help
	typ     string
}

func (v *settingValue) String() string { return v.current }
func (v *settingValue) Type() string   { return v.typ }
func (v *settingValue) Set(raw string) error {
	if _, err := parseScalar(v.fd, raw); err != nil {
		return err
	}
	v.current = raw
	return nil
}

// flagTypeName returns a short type label rendered in --help next to the flag name. Matches pflag's convention.
func flagTypeName(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.StringKind, protoreflect.EnumKind:
		return "string"
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	}
	return "string"
}

// RegisterFlags binds every cli_flag annotation in bindings onto fs. The flag's default text is the binding's parsed
// default (rendered via the value's String); the description becomes the usage string.
//
// Bool fields get NoOptDefVal="true" so users can write `--flag` instead of `--flag=true`. Negation (`--flag=false`)
// stays available.
func RegisterFlags(fs *pflag.FlagSet, bindings []Binding) {
	for _, b := range bindings {
		if b.FlagName == "" {
			continue
		}
		v := &settingValue{
			fd:      b.Field,
			current: defaultDisplay(b),
			typ:     flagTypeName(b.Field),
		}
		if b.FlagShort != "" {
			fs.VarP(v, b.FlagName, b.FlagShort, b.Description)
		} else {
			fs.Var(v, b.FlagName, b.Description)
		}
		if b.Field.Kind() == protoreflect.BoolKind {
			fs.Lookup(b.FlagName).NoOptDefVal = "true"
		}
	}
}

// defaultDisplay renders the binding's default for --help. Empty default ⇒ empty string (pflag will display it as "").
func defaultDisplay(b Binding) string {
	if !b.Default.IsValid() {
		return ""
	}
	switch b.Field.Kind() {
	case protoreflect.EnumKind:
		ev := b.Field.Enum().Values().ByNumber(b.Default.Enum())
		if ev != nil {
			return string(ev.Name())
		}
		return ""
	}
	return b.Default.String()
}
