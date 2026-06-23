package main

import "testing"

func TestPodUIDFromSegment(t *testing.T) {
	cases := []struct {
		seg, want string
	}{
		// systemd (cgroup v2): underscores normalize back to the dashed UID.
		{"kubepods-burstable-pod12345678_1234_1234_1234_123456789abc.slice",
			"12345678-1234-1234-1234-123456789abc"},
		// cgroupfs (v1): already dashed.
		{"pod12345678-1234-1234-1234-123456789abc",
			"12345678-1234-1234-1234-123456789abc"},
		// not a pod segment
		{"kubepods.slice", ""},
		{"docker", ""},
	}
	for _, c := range cases {
		if got := podUIDFromSegment(c.seg); got != c.want {
			t.Errorf("podUIDFromSegment(%q) = %q, want %q", c.seg, got, c.want)
		}
	}
}

func TestContainerIDFromSegment(t *testing.T) {
	id := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" // 64
	cases := []struct {
		seg, want string
	}{
		{"cri-containerd-" + id + ".scope", id},
		{"docker-" + id + ".scope", id},
		{id, id},
		{"short", ""},
	}
	for _, c := range cases {
		if got := containerIDFromSegment(c.seg); got != c.want {
			t.Errorf("containerIDFromSegment(%q) = %q, want %q", c.seg, got, c.want)
		}
	}
}
