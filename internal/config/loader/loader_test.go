package loader

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	fixturepb "claude-auto-permission/internal/gen/loadertest/v1"
)

// fixtureBindings is a convenience: walks the Fixture descriptor and returns the binding registry tests can inspect or
// pass through to Apply* helpers without re-walking each time.
func fixtureBindings(t *testing.T) []Binding {
	t.Helper()
	bs, err := Walk((&fixturepb.Fixture{}).ProtoReflect().Descriptor())
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return bs
}

func TestWalk_DiscoversEveryAnnotatedField(t *testing.T) {
	bs := fixtureBindings(t)

	got := map[string]Binding{}
	for _, b := range bs {
		got[strings.Join(b.Path, ".")] = b
	}

	wantPaths := []string{
		"inner.nested_field",
		"str_field",
		"bool_field",
		"int32_field",
		"int64_field",
		"float_field",
		"double_field",
		"enum_field",
		"override_default_name",
	}
	gotPaths := make([]string, 0, len(got))
	for p := range got {
		gotPaths = append(gotPaths, p)
	}
	sort.Strings(wantPaths)
	sort.Strings(gotPaths)
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Errorf("walked paths = %v, want %v", gotPaths, wantPaths)
	}
}

func TestWalk_DerivesEnvAndFlagNames(t *testing.T) {
	bs := fixtureBindings(t)
	byPath := map[string]Binding{}
	for _, b := range bs {
		byPath[strings.Join(b.Path, ".")] = b
	}

	tests := []struct {
		path     string
		wantEnv  string
		wantFlag string
		wantShrt string
	}{
		{"str_field", "CLAUDE_AUTO_PERMISSION_STR_FIELD", "str-field", ""},
		{"bool_field", "CLAUDE_AUTO_PERMISSION_BOOL_FIELD", "bool-field", ""},
		{"inner.nested_field", "CLAUDE_AUTO_PERMISSION_INNER_NESTED_FIELD", "nested-field", ""},
		{"override_default_name", "FIXTURE_CUSTOM_ENV", "custom-flag", "c"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			b, ok := byPath[tt.path]
			if !ok {
				t.Fatalf("no binding for %s", tt.path)
			}
			if b.EnvName != tt.wantEnv {
				t.Errorf("EnvName = %q, want %q", b.EnvName, tt.wantEnv)
			}
			if b.FlagName != tt.wantFlag {
				t.Errorf("FlagName = %q, want %q", b.FlagName, tt.wantFlag)
			}
			if b.FlagShort != tt.wantShrt {
				t.Errorf("FlagShort = %q, want %q", b.FlagShort, tt.wantShrt)
			}
		})
	}
}

func TestApplyDefaults_EveryKind(t *testing.T) {
	bs := fixtureBindings(t)
	msg := &fixturepb.Fixture{}
	if err := ApplyDefaults(msg, bs); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}

	if got := msg.GetStrField(); got != "hello" {
		t.Errorf("str_field = %q, want hello", got)
	}
	if got := msg.GetBoolField(); !got {
		t.Errorf("bool_field = false, want true")
	}
	if got := msg.GetInt32Field(); got != 42 {
		t.Errorf("int32_field = %d, want 42", got)
	}
	if got := msg.GetInt64Field(); got != 9000000000 {
		t.Errorf("int64_field = %d, want 9000000000", got)
	}
	if got := msg.GetFloatField(); got != 1.5 {
		t.Errorf("float_field = %v, want 1.5", got)
	}
	if got := msg.GetDoubleField(); got != 2.5 {
		t.Errorf("double_field = %v, want 2.5", got)
	}
	if got := msg.GetEnumField(); got != fixturepb.Color_COLOR_RED {
		t.Errorf("enum_field = %v, want COLOR_RED", got)
	}
	if got := msg.GetInner().GetNestedField(); got != "nested-default" {
		t.Errorf("inner.nested_field = %q, want nested-default", got)
	}
}

func TestFillDefaults_SkipsExplicitlySetFields(t *testing.T) {
	msg := &fixturepb.Fixture{}
	msg.SetInt32Field(0) // explicitly set to zero
	msg.SetStrField("custom")

	if err := FillDefaults(msg); err != nil {
		t.Fatalf("FillDefaults: %v", err)
	}

	// Explicitly set fields are preserved even when zero.
	if got := msg.GetInt32Field(); got != 0 {
		t.Errorf("int32_field = %d, want 0 (explicitly set)", got)
	}
	if got := msg.GetStrField(); got != "custom" {
		t.Errorf("str_field = %q, want custom", got)
	}
	// Unset fields receive their schema defaults.
	if got := msg.GetBoolField(); !got {
		t.Errorf("bool_field = false, want true (schema default)")
	}
	if got := msg.GetInt64Field(); got != 9000000000 {
		t.Errorf("int64_field = %d, want 9000000000 (schema default)", got)
	}
}

func TestFillDefaults_EnumZeroTakesDefault(t *testing.T) {
	tests := []struct {
		name     string
		explicit bool // false = leave the field unset
		value    fixturepb.Color
		want     fixturepb.Color
	}{
		{name: "unset", explicit: false, want: fixturepb.Color_COLOR_RED},
		{name: "explicit UNSPECIFIED", explicit: true, value: fixturepb.Color_COLOR_UNSPECIFIED, want: fixturepb.Color_COLOR_RED},
		{name: "explicit real value preserved", explicit: true, value: fixturepb.Color_COLOR_BLUE, want: fixturepb.Color_COLOR_BLUE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &fixturepb.Fixture{}
			if tt.explicit {
				msg.SetEnumField(tt.value)
			}
			if err := FillDefaults(msg); err != nil {
				t.Fatalf("FillDefaults: %v", err)
			}
			if got := msg.GetEnumField(); got != tt.want {
				t.Errorf("enum_field = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolve_PrecedenceLayers(t *testing.T) {
	configBody := []byte(`str_field: "from-file"` + "\n" +
		`int32_field: 100` + "\n")
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.txtpb")
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	tests := []struct {
		name      string
		args      []string
		env       map[string]string
		wantStr   string
		wantInt32 int32
		wantBool  bool
	}{
		{
			name:      "defaults only",
			args:      nil,
			env:       nil,
			wantStr:   "hello",
			wantInt32: 42,
			wantBool:  true,
		},
		{
			name:      "file overrides default",
			args:      []string{"--config", configPath},
			env:       nil,
			wantStr:   "from-file",
			wantInt32: 100,
			wantBool:  true, // file didn't set bool_field
		},
		{
			name:      "env overrides file",
			args:      []string{"--config", configPath},
			env:       map[string]string{"CLAUDE_AUTO_PERMISSION_STR_FIELD": "from-env"},
			wantStr:   "from-env",
			wantInt32: 100, // file still wins for int32
			wantBool:  true,
		},
		{
			name: "flag overrides env and file",
			args: []string{"--config", configPath, "--str-field", "from-flag", "--bool-field=false"},
			env: map[string]string{
				"CLAUDE_AUTO_PERMISSION_STR_FIELD": "from-env",
			},
			wantStr:   "from-flag",
			wantInt32: 100,
			wantBool:  false,
		},
		{
			name:      "env-only path resolves the config file",
			args:      []string{},
			env:       map[string]string{"CLAUDE_AUTO_PERMISSION_CONFIG": configPath},
			wantStr:   "from-file",
			wantInt32: 100,
			wantBool:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &fixturepb.Fixture{}
			if err := Resolve(msg, Options{Args: tt.args, Env: tt.env}); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := msg.GetStrField(); got != tt.wantStr {
				t.Errorf("str_field = %q, want %q", got, tt.wantStr)
			}
			if got := msg.GetInt32Field(); got != tt.wantInt32 {
				t.Errorf("int32_field = %d, want %d", got, tt.wantInt32)
			}
			if got := msg.GetBoolField(); got != tt.wantBool {
				t.Errorf("bool_field = %v, want %v", got, tt.wantBool)
			}
		})
	}
}

func TestResolve_BareBoolFlagSetsTrue(t *testing.T) {
	// `--bool-field` (no value) is the conventional shorthand for `--bool-field=true`. The fixture's bool_field default is
	// true, so we use a fixture where the default is implicitly false to confirm the bare flag flipped it.
	msg := &fixturepb.Fixture{}
	err := Resolve(msg, Options{
		Args: []string{"--bool-field=false", "--bool-field"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !msg.GetBoolField() {
		t.Errorf("bool_field = false, want true (bare --bool-field should set true)")
	}
}

func TestResolve_OverrideFlagAndEnvNames(t *testing.T) {
	msg := &fixturepb.Fixture{}
	err := Resolve(msg, Options{
		Args: []string{"--custom-flag", "via-override-flag"},
	})
	if err != nil {
		t.Fatalf("Resolve flag: %v", err)
	}
	if got := msg.GetOverrideDefaultName(); got != "via-override-flag" {
		t.Errorf("override_default_name via flag = %q, want via-override-flag", got)
	}

	msg = &fixturepb.Fixture{}
	err = Resolve(msg, Options{
		Env: map[string]string{"FIXTURE_CUSTOM_ENV": "via-override-env"},
	})
	if err != nil {
		t.Fatalf("Resolve env: %v", err)
	}
	if got := msg.GetOverrideDefaultName(); got != "via-override-env" {
		t.Errorf("override_default_name via env = %q, want via-override-env", got)
	}
}

func TestResolve_MissingConfigFileTolerated(t *testing.T) {
	msg := &fixturepb.Fixture{}
	err := Resolve(msg, Options{
		Args: []string{"--config", "/nonexistent/path.txtpb"},
		Env:  map[string]string{"CLAUDE_AUTO_PERMISSION_STR_FIELD": "from-env"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// File missing ⇒ defaults applied, env still wins over them.
	if got := msg.GetStrField(); got != "from-env" {
		t.Errorf("str_field = %q, want from-env", got)
	}
}

func TestResolve_ParseError(t *testing.T) {
	msg := &fixturepb.Fixture{}
	err := Resolve(msg, Options{
		Env: map[string]string{"CLAUDE_AUTO_PERMISSION_INT32_FIELD": "not-a-number"},
	})
	if err == nil {
		t.Fatal("expected error from non-numeric env value")
	}
	if !strings.Contains(err.Error(), "INT32_FIELD") {
		t.Errorf("error = %v, want it to mention INT32_FIELD", err)
	}
}

func TestResolve_FlagNotSetUsesPriorLayer(t *testing.T) {
	// The flag is registered with the schema default; if the user doesn't pass it on the command line, ApplyFlags must NOT
	// clobber the env/file value.
	msg := &fixturepb.Fixture{}
	err := Resolve(msg, Options{
		Args: []string{}, // no flags set explicitly
		Env:  map[string]string{"CLAUDE_AUTO_PERMISSION_STR_FIELD": "from-env"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := msg.GetStrField(); got != "from-env" {
		t.Errorf("str_field = %q, want from-env", got)
	}
}

func TestEnvSlice(t *testing.T) {
	got := EnvSlice([]string{"FOO=bar", "BAZ=qux=more", "BARE", ""})
	want := map[string]string{"FOO": "bar", "BAZ": "qux=more"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnvSlice = %v, want %v", got, want)
	}
}

// TestWalk_RejectsAnnotatedNonScalar builds a synthetic descriptor with a repeated string carrying a FieldOpts
// annotation and asserts Walk returns an error. The fixture proto can't include such a field (Walk would fail on every
// test run); instead we construct a minimal MessageDescriptor on the fly.
func TestWalk_RejectsAnnotatedNonScalar(t *testing.T) {
	// Build a Fixture but reach into its descriptor to find a list-shaped field — the fixture has none, so we test the
	// rejection path via a different mechanism: a hand-rolled negative fixture. See proto/loadertest/v1/badfixture.proto
	// for the invalid schema; if Walk ever stops rejecting it, this test fails.
	bs, err := Walk((&fixturepb.BadFixture{}).ProtoReflect().Descriptor())
	if err == nil {
		t.Fatalf("Walk on BadFixture returned %d bindings, want error", len(bs))
	}
	if !strings.Contains(err.Error(), "scalar singular fields") {
		t.Errorf("error = %v, want it to mention 'scalar singular fields'", err)
	}
}

// TestApplyDefaults_DoesNotPanicOnEmptyMessage exercises the edge case where the schema has no annotations at all.
func TestApplyDefaults_DoesNotPanicOnEmptyMessage(t *testing.T) {
	if err := ApplyDefaults(&fixturepb.Fixture{}, nil); err != nil {
		t.Errorf("ApplyDefaults nil bindings: %v", err)
	}
}

// guard: keep proto import live in case we add more granular tests.
var _ proto.Message = (*fixturepb.Fixture)(nil)
