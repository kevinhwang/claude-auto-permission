package policy

import (
	"testing"

	"claude-auto-permission/internal/config"
	configpb "claude-auto-permission/internal/gen/config/v1"
)

func TestIsWriteAllowed(t *testing.T) {
	c := configpb.Config_builder{
		Projects: []*configpb.Project{
			configpb.Project_builder{
				PathPatterns: []string{"/**"},
				StaticBashRules: configpb.StaticBashRules_builder{
					AllowWritePatterns: []string{"/tmp/**"},
				}.Build(),
			}.Build(),
			configpb.Project_builder{
				PathPatterns: []string{"/home/user/src/server/**"},
				StaticBashRules: configpb.StaticBashRules_builder{
					AllowProjectWrite:  ptr(true),
					AllowWritePatterns: []string{"/var/data/**"},
				}.Build(),
			}.Build(),
		},
	}.Build()

	tests := []struct {
		name        string
		path        string
		cwd         string
		projectRoot string
		allow       bool
	}{
		{name: "/tmp from anywhere", path: "/tmp/file.txt", cwd: "/home/user/other", projectRoot: "/home/user/other", allow: true},
		{name: "/tmp nested", path: "/tmp/deep/file", cwd: "/", projectRoot: "/", allow: true},
		{name: "/etc denied", path: "/etc/passwd", cwd: "/home/user", projectRoot: "/home/user", allow: false},
		{name: "project write", path: "/home/user/src/server/output.txt", cwd: "/home/user/src/server", projectRoot: "/home/user/src/server", allow: true},
		{name: "project subdir write", path: "/home/user/src/server/pkg/file", cwd: "/home/user/src/server", projectRoot: "/home/user/src/server", allow: true},
		{name: "project write from subdir cwd", path: "/home/user/src/server/file", cwd: "/home/user/src/server/pkg", projectRoot: "/home/user/src/server", allow: true},
		{name: "project write parent dir", path: "/home/user/src/server/other/file", cwd: "/home/user/src/server/pkg", projectRoot: "/home/user/src/server", allow: true},
		{name: "/var/data from server project", path: "/var/data/file", cwd: "/home/user/src/server", projectRoot: "/home/user/src/server", allow: true},
		{name: "/var/data from other cwd", path: "/var/data/file", cwd: "/home/user/other", projectRoot: "/home/user/other", allow: false},
		{name: "outside project", path: "/home/user/other/file", cwd: "/home/user/src/server", projectRoot: "/home/user/src/server", allow: false},
		{name: "global allow_project_write does not mean write-anywhere", path: "/etc/passwd", cwd: "/home/user/src/server", projectRoot: "/home/user/src/server", allow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWriteAllowed(config.NewResolver(c), tt.path, tt.cwd, tt.projectRoot); got != tt.allow {
				t.Errorf("IsWriteAllowed(%q, cwd=%q, projectRoot=%q) = %v, want %v", tt.path, tt.cwd, tt.projectRoot, got, tt.allow)
			}
		})
	}
}

func TestMatchRemoteHost(t *testing.T) {
	c := configpb.Config_builder{
		Projects: []*configpb.Project{
			configpb.Project_builder{
				PathPatterns: []string{"/**"},
			}.Build(),
			configpb.Project_builder{
				PathPatterns: []string{"/home/user/src/server/**"},
				StaticBashRules: configpb.StaticBashRules_builder{
					RemoteHosts: []*configpb.RemoteHost{
						configpb.RemoteHost_builder{
							HostPatterns:       []string{"example.com", "*.example.com"},
							AllowWritePatterns: []string{"/tmp/**"},
						}.Build(),
					},
				}.Build(),
			}.Build(),
		},
	}.Build()

	tests := []struct {
		name  string
		host  string
		cwd   string
		found bool
	}{
		{name: "exact match", host: "example.com", cwd: "/home/user/src/server", found: true},
		{name: "wildcard match", host: "foo.example.com", cwd: "/home/user/src/server", found: true},
		{name: "no match host", host: "evil.com", cwd: "/home/user/src/server", found: false},
		{name: "right host wrong cwd", host: "example.com", cwd: "/home/user/other", found: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := MatchRemoteHost(config.NewResolver(c), tt.host, tt.cwd)
			if ok != tt.found {
				t.Errorf("MatchRemoteHost(%q, cwd=%q) found = %v, want %v", tt.host, tt.cwd, ok, tt.found)
			}
		})
	}
}

func TestUnionSemantics(t *testing.T) {
	c := configpb.Config_builder{
		Projects: []*configpb.Project{
			configpb.Project_builder{
				PathPatterns: []string{"/**"},
				StaticBashRules: configpb.StaticBashRules_builder{
					AllowWritePatterns: []string{"/tmp/**"},
				}.Build(),
			}.Build(),
			configpb.Project_builder{
				PathPatterns: []string{"/home/user/src/server/**"},
				StaticBashRules: configpb.StaticBashRules_builder{
					AllowProjectWrite: ptr(true),
				}.Build(),
			}.Build(),
		},
	}.Build()

	cwd := "/home/user/src/server"
	projectRoot := "/home/user/src/server"
	r := config.NewResolver(c)
	if !IsWriteAllowed(r, "/tmp/file", cwd, projectRoot) {
		t.Error("/tmp should be allowed via first project")
	}
	if !IsWriteAllowed(r, "/home/user/src/server/file", cwd, projectRoot) {
		t.Error("project dir should be allowed via second project")
	}
	if IsWriteAllowed(r, "/etc/passwd", cwd, projectRoot) {
		t.Error("/etc should be denied by both projects")
	}
}

func ptr[T any](v T) *T { return &v }
