package sfveritas

import (
	"os"
	"testing"
)

// --- detectInfra ---

func TestDetectInfra_BareMetal(t *testing.T) {
	os.Unsetenv("KUBERNETES_SERVICE_HOST")
	infraT, details := detectInfra()
	if infraT != infraBareMetal {
		t.Errorf("expected bare_metal, got %s", infraT)
	}
	if details["os"] == nil {
		t.Error("expected 'os' in details")
	}
	if details["arch"] == nil {
		t.Error("expected 'arch' in details")
	}
	if details["numCPU"] == nil {
		t.Error("expected 'numCPU' in details")
	}
	if details["goVer"] == nil {
		t.Error("expected 'goVer' in details")
	}
}

func TestDetectInfra_Kubernetes(t *testing.T) {
	os.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	os.Setenv("HOSTNAME", "my-pod-abc")
	os.Setenv("POD_NAMESPACE", "production")
	defer func() {
		os.Unsetenv("KUBERNETES_SERVICE_HOST")
		os.Unsetenv("HOSTNAME")
		os.Unsetenv("POD_NAMESPACE")
	}()

	infraT, details := detectInfra()
	if infraT != infraKubernetes {
		t.Errorf("expected kubernetes, got %s", infraT)
	}
	if details["podName"] != "my-pod-abc" {
		t.Errorf("expected podName=my-pod-abc, got %v", details["podName"])
	}
	if details["namespace"] != "production" {
		t.Errorf("expected namespace=production, got %v", details["namespace"])
	}
}

// --- infraType constants ---

func TestInfraTypeConstants(t *testing.T) {
	if string(infraDocker) != "docker" {
		t.Errorf("expected 'docker', got %s", infraDocker)
	}
	if string(infraKubernetes) != "kubernetes" {
		t.Errorf("expected 'kubernetes', got %s", infraKubernetes)
	}
	if string(infraBareMetal) != "bare_metal" {
		t.Errorf("expected 'bare_metal', got %s", infraBareMetal)
	}
}
