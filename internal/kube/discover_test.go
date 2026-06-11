package kube

import (
	"context"
	"reflect"
	"testing"
)

func TestDiscoveryArgs(t *testing.T) {
	if got, want := contextsArgs(), []string{"config", "get-contexts", "-o", "name"}; !reflect.DeepEqual(got, want) {
		t.Errorf("contextsArgs() = %q, want %q", got, want)
	}
	if got, want := currentContextArgs(), []string{"config", "current-context"}; !reflect.DeepEqual(got, want) {
		t.Errorf("currentContextArgs() = %q, want %q", got, want)
	}
	if got, want := namespacesArgs(), []string{"get", "namespaces", "-o", "name"}; !reflect.DeepEqual(got, want) {
		t.Errorf("namespacesArgs() = %q, want %q", got, want)
	}

	wantPods := []string{"get", "pods", "-n", "prod", "-o", "jsonpath=" + podsJSONPath}
	if got := podsArgs("prod"); !reflect.DeepEqual(got, wantPods) {
		t.Errorf("podsArgs(prod) = %q, want %q", got, wantPods)
	}
	// No namespace omits the -n flag.
	wantNoNS := []string{"get", "pods", "-o", "jsonpath=" + podsJSONPath}
	if got := podsArgs(""); !reflect.DeepEqual(got, wantNoNS) {
		t.Errorf("podsArgs(\"\") = %q, want %q", got, wantNoNS)
	}
}

func TestDiscoveryCmdBin(t *testing.T) {
	d := &Discovery{Bin: "/usr/local/bin/kubectl"}
	got := d.cmd(context.Background(), "", "get", "pods").Args
	want := []string{"/usr/local/bin/kubectl", "get", "pods"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cmd args = %q, want %q", got, want)
	}
	if got := (&Discovery{}).bin(); got != "kubectl" {
		t.Errorf("default bin = %q, want kubectl", got)
	}
}

func TestDiscoveryCmdContext(t *testing.T) {
	got := (&Discovery{}).cmd(context.Background(), "staging", "get", "namespaces", "-o", "name").Args
	want := []string{"kubectl", "--context", "staging", "get", "namespaces", "-o", "name"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cmd args = %q, want %q", got, want)
	}
}

func TestParseNames(t *testing.T) {
	out := "namespace/default\nnamespace/kube-system\n\nnamespace/prod\n"
	got := parseNames(out, "namespace/")
	want := []string{"default", "kube-system", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseNames = %q, want %q", got, want)
	}
	if got := parseNames("", "namespace/"); got != nil {
		t.Errorf("parseNames(empty) = %q, want nil", got)
	}
}

func TestParsePods(t *testing.T) {
	// As emitted by podsJSONPath: name, tab, space-separated containers with a
	// trailing space, one pod per line.
	out := "nginx\tapp \nredis\tserver exporter \nlonely\t\n"
	got := parsePods(out)
	want := []PodInfo{
		{Name: "nginx", Containers: []string{"app"}},
		{Name: "redis", Containers: []string{"server", "exporter"}},
		{Name: "lonely"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePods =\n  %#v\nwant\n  %#v", got, want)
	}
	if got := parsePods(""); got != nil {
		t.Errorf("parsePods(empty) = %#v, want nil", got)
	}
}
