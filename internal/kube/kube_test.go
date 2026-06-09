package kube

import (
	"reflect"
	"testing"
)

func TestClientExecArgs(t *testing.T) {
	tests := []struct {
		name   string
		client Client
		stdin  bool
		cmd    []string
		want   []string
	}{
		{
			name:   "minimal",
			client: Client{Pod: "web"},
			cmd:    []string{"ls", "-la"},
			want:   []string{"kubectl", "exec", "web", "--", "ls", "-la"},
		},
		{
			name:   "namespace, container and stdin",
			client: Client{Namespace: "prod", Pod: "web", Container: "app"},
			stdin:  true,
			cmd:    []string{"tar", "-x"},
			want:   []string{"kubectl", "-n", "prod", "exec", "-i", "web", "-c", "app", "--", "tar", "-x"},
		},
		{
			name:   "bin override",
			client: Client{Pod: "web", Bin: "/usr/local/bin/kubectl"},
			cmd:    []string{"id"},
			want:   []string{"/usr/local/bin/kubectl", "exec", "web", "--", "id"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.client.Exec(tt.stdin, tt.cmd...).Args
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Exec args =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}
