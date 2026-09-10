package main

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// allowedRegistries are the only places a container image may come from.
// docker.io covers unqualified images (nginx, nginxinc/nginx-unprivileged)
// since that's Docker's implicit default when no registry is named.
var allowedRegistries = []string{
	"docker.io",
	"ghcr.io/davidpotters",
}

// checkPolicy runs every container in the pod (including init containers --
// a policy that only checked spec.containers would let a malicious init
// container run as root before the "real" containers even start) against
// the four rules from the README: resource limits required, no root
// containers, no :latest tags, registry allow-list. Returns one message per
// violation found, not just the first -- a rejected pod should tell you
// everything wrong with it in one shot, not make you fix-and-resubmit
// repeatedly to discover the next problem.
func checkPolicy(pod *corev1.Pod) []string {
	var violations []string

	allContainers := append([]corev1.Container{}, pod.Spec.InitContainers...)
	allContainers = append(allContainers, pod.Spec.Containers...)

	for _, c := range allContainers {
		if v := checkResourceLimits(c); v != "" {
			violations = append(violations, v)
		}
		if v := checkNonRoot(pod, c); v != "" {
			violations = append(violations, v)
		}
		if v := checkImage(c); v != "" {
			violations = append(violations, v)
		}
	}

	return violations
}

func checkResourceLimits(c corev1.Container) string {
	cpu := c.Resources.Limits.Cpu()
	mem := c.Resources.Limits.Memory()
	if cpu.IsZero() || mem.IsZero() {
		return fmt.Sprintf("container %q must set resource limits for both cpu and memory", c.Name)
	}
	return ""
}

// checkNonRoot requires an explicit runAsNonRoot: true, at either the
// container or pod level (container-level wins when both are set, matching
// how Kubernetes itself resolves it). Unset is treated the same as false --
// silence isn't consent here, since the default behavior of most images
// (root) is exactly what this policy exists to block.
func checkNonRoot(pod *corev1.Pod, c corev1.Container) string {
	if c.SecurityContext != nil && c.SecurityContext.RunAsNonRoot != nil {
		if !*c.SecurityContext.RunAsNonRoot {
			return fmt.Sprintf("container %q must set securityContext.runAsNonRoot: true", c.Name)
		}
		return ""
	}
	if pod.Spec.SecurityContext != nil && pod.Spec.SecurityContext.RunAsNonRoot != nil {
		if !*pod.Spec.SecurityContext.RunAsNonRoot {
			return fmt.Sprintf("container %q must set securityContext.runAsNonRoot: true", c.Name)
		}
		return ""
	}
	return fmt.Sprintf("container %q must explicitly set securityContext.runAsNonRoot: true (pod or container level)", c.Name)
}

func checkImage(c corev1.Container) string {
	ref, err := parseImageRef(c.Image)
	if err != nil {
		return fmt.Sprintf("container %q has an unparseable image reference %q: %v", c.Name, c.Image, err)
	}

	if !ref.pinned {
		return fmt.Sprintf("container %q image %q must not use the :latest tag (or no tag, which means :latest) -- pin a version or digest", c.Name, c.Image)
	}

	allowed := false
	for _, r := range allowedRegistries {
		if ref.repository == r || strings.HasPrefix(ref.repository, r+"/") {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Sprintf("container %q image %q (repository %q) isn't on the allow-list (%s)", c.Name, c.Image, ref.repository, strings.Join(allowedRegistries, ", "))
	}

	return ""
}

type imageRef struct {
	registry   string // bare host: "docker.io", "ghcr.io", "localhost:5000"
	repository string // registry + full path, no tag/digest: "ghcr.io/davidpotters/guardrail-webhook"
	pinned     bool   // true if the image is locked to a real tag or a digest, not :latest / implicit latest
}

// parseImageRef pulls apart a container image string enough to answer the
// two questions the policy needs -- what registry is this from, and is it
// actually pinned -- without pulling in Docker's own reference-parsing
// library for what's a genuinely small grammar. The trap this has to avoid:
// a registry host with a port (localhost:5000/app) contains a colon that
// looks exactly like a tag separator but isn't one.
func parseImageRef(image string) (imageRef, error) {
	if image == "" {
		return imageRef{}, fmt.Errorf("empty image reference")
	}

	// Digest pins (name@sha256:...) are strictly stronger than any tag --
	// the exact bytes are addressed, not a mutable label -- so they satisfy
	// "pinned" outright regardless of whatever tag might also be present.
	namePart := image
	pinned := false
	if at := strings.Index(image, "@"); at != -1 {
		namePart = image[:at]
		pinned = true
	}

	firstSlash := strings.Index(namePart, "/")
	var registry, remainder string
	if firstSlash == -1 {
		// No slash at all: a bare name like "nginx" -- implicit Docker Hub,
		// implicit "library/" namespace.
		registry = "docker.io"
		remainder = namePart
	} else {
		candidate := namePart[:firstSlash]
		// A real registry host contains a "." (a domain) or a ":" (a port),
		// or is literally "localhost". Anything else -- "nginxinc",
		// "davidpotters" -- is a Docker Hub user/org namespace, not a host.
		if strings.ContainsAny(candidate, ".:") || candidate == "localhost" {
			registry = candidate
			remainder = namePart[firstSlash+1:]
		} else {
			registry = "docker.io"
			remainder = namePart
		}
	}

	// Only a colon in the last path segment is a tag separator -- a
	// registry port was already peeled off above, so any colon left here is
	// genuinely the tag, and needs stripping to get a clean repository path.
	lastSlash := strings.LastIndex(remainder, "/")
	lastSegment := remainder[lastSlash+1:]
	if colon := strings.LastIndex(lastSegment, ":"); colon != -1 {
		tag := lastSegment[colon+1:]
		if !pinned {
			pinned = tag != "latest" && tag != ""
		}
		remainder = remainder[:lastSlash+1] + lastSegment[:colon]
	}
	// No colon in the last segment at all means no tag was given, which
	// Docker resolves to :latest -- pinned stays whatever digest-pinning
	// already determined above (false, unless "@..." was present).

	return imageRef{
		registry:   registry,
		repository: registry + "/" + remainder,
		pinned:     pinned,
	}, nil
}
