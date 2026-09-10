package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func containerWithImage(image string) corev1.Container {
	return corev1.Container{Name: "test", Image: image}
}

func boolPtr(b bool) *bool { return &b }

func TestCheckResourceLimits(t *testing.T) {
	noLimits := corev1.Container{Name: "test"}
	if got := checkResourceLimits(noLimits); got == "" {
		t.Error("expected a violation for missing resource limits, got none")
	}

	cpuOnly := corev1.Container{
		Name: "test",
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
		},
	}
	if got := checkResourceLimits(cpuOnly); got == "" {
		t.Error("expected a violation for cpu-only limits (memory missing), got none")
	}

	both := corev1.Container{
		Name: "test",
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
	}
	if got := checkResourceLimits(both); got != "" {
		t.Errorf("expected no violation with both limits set, got: %s", got)
	}
}

func TestCheckNonRoot(t *testing.T) {
	podWithSC := func(runAsNonRoot *bool) *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: runAsNonRoot},
		}}
	}

	cases := []struct {
		name      string
		pod       *corev1.Pod
		container corev1.Container
		wantEmpty bool
	}{
		{"unset at both levels rejected", &corev1.Pod{}, corev1.Container{Name: "c"}, false},
		{"pod-level true passes", podWithSC(boolPtr(true)), corev1.Container{Name: "c"}, true},
		{"pod-level false rejected", podWithSC(boolPtr(false)), corev1.Container{Name: "c"}, false},
		{
			"container-level overrides pod-level false",
			podWithSC(boolPtr(false)),
			corev1.Container{Name: "c", SecurityContext: &corev1.SecurityContext{RunAsNonRoot: boolPtr(true)}},
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkNonRoot(tc.pod, tc.container)
			if tc.wantEmpty && got != "" {
				t.Errorf("expected no violation, got: %s", got)
			}
			if !tc.wantEmpty && got == "" {
				t.Error("expected a violation, got none")
			}
		})
	}
}

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		image        string
		wantRegistry string
		wantPinned   bool
	}{
		{"nginx", "docker.io", false},
		{"nginx:1.27", "docker.io", true},
		{"nginx:latest", "docker.io", false},
		{"nginxinc/nginx-unprivileged:1.27-alpine", "docker.io", true},
		{"nginxinc/nginx-unprivileged", "docker.io", false},
		// The trap: a registry port looks exactly like a tag separator.
		{"localhost:5000/myapp", "localhost:5000", false},
		{"localhost:5000/myapp:v1", "localhost:5000", true},
		{"ghcr.io/davidpotters/guardrail-webhook:v1", "ghcr.io", true},
		{"ghcr.io/davidpotters/guardrail-webhook", "ghcr.io", false},
		// Digest pins are stronger than any tag, and satisfy "pinned" even
		// with no tag present at all.
		{"nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "docker.io", true},
		{"gcr.io/some-project/image:v1", "gcr.io", true},
	}

	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			ref, err := parseImageRef(tc.image)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref.registry != tc.wantRegistry {
				t.Errorf("registry = %q, want %q", ref.registry, tc.wantRegistry)
			}
			if ref.pinned != tc.wantPinned {
				t.Errorf("pinned = %v, want %v", ref.pinned, tc.wantPinned)
			}
		})
	}
}

func TestParseImageRefEmpty(t *testing.T) {
	if _, err := parseImageRef(""); err == nil {
		t.Error("expected an error for an empty image reference, got nil")
	}
}

func TestCheckImage(t *testing.T) {
	cases := []struct {
		name      string
		image     string
		wantEmpty bool // true if the image should pass (no violation)
	}{
		{"pinned dockerhub allowed", "nginxinc/nginx-unprivileged:1.27-alpine", true},
		{"unqualified latest rejected", "nginx", false},
		{"explicit latest rejected", "nginx:latest", false},
		{"our own registry allowed", "ghcr.io/davidpotters/guardrail-webhook:v1", true},
		{"someone else's registry rejected", "ghcr.io/someone-else/thing:v1", false},
		{"unlisted registry rejected", "gcr.io/some-project/image:v1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := containerWithImage(tc.image)
			got := checkImage(c)
			if tc.wantEmpty && got != "" {
				t.Errorf("expected no violation for %q, got: %s", tc.image, got)
			}
			if !tc.wantEmpty && got == "" {
				t.Errorf("expected a violation for %q, got none", tc.image)
			}
		})
	}
}
